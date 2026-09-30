#!/usr/bin/env python3
"""
organize_takeout.py - Unpack Google Photos Takeout zips and sort files by date.

What it does
  * Reads every .zip in SOURCE (all parts of the Takeout export).
  * Uses the JSON sidecar files ONLY to work out each photo's real "taken" date,
    then discards them (no JSON is written to the output).
  * Streams media straight from the zips into DEST/<year>/<month>/ (no
    intermediate extraction, so you only need space for the final photos).
  * Sets each file's modified time to its taken date.
  * Skips exact duplicates (Takeout repeats photos that live in albums).
  * Safe to re-run: already-written files are recognised and skipped.

Date lookup order
  1. JSON sidecar (photoTakenTime, then creationTime)
  2. EXIF data (only if Pillow is installed: pip install pillow)
  3. A date in the filename, e.g. IMG_20200115_101500.jpg
  4. Otherwise the file goes to DEST/Unknown_Date/

Usage
  python organize_takeout.py ~/Downloads/takeout ~/Photos-Backup
  python organize_takeout.py ~/Downloads/takeout ~/Photos-Backup --dry-run
  python organize_takeout.py SRC DEST --structure year-month-day
"""

from __future__ import annotations

import argparse
import io
import json
import os
import re
import shutil
import sys
import zipfile
import zlib
from collections import Counter
from datetime import UTC, datetime
from pathlib import Path, PurePosixPath

try:
    from PIL import Image  # optional, used only as an EXIF fallback
except ImportError:
    Image = None

SKIP_EXT = {".json", ".html"}  # metadata / Takeout's archive browser page
EXIF_EXTS = {".jpg", ".jpeg", ".tif", ".tiff"}
MAX_EXIF_BYTES = 100 * 1024 * 1024
DUP_RE = re.compile(r"^(?P<stem>.*)\((?P<n>\d+)\)(?P<ext>\.[^.]*)?$")
JSON_DUP_RE = re.compile(r"\((\d+)\)\.json$", re.IGNORECASE)
NAME_DATE_RE = re.compile(
    r"(?<!\d)(19[89]\d|20[0-4]\d)[-_.]?(0[1-9]|1[0-2])[-_.]?(0[1-9]|[12]\d|3[01])(?!\d)"
)


# --------------------------------------------------------------------------
# Pass 1: build an index of dates from all JSON sidecars
# --------------------------------------------------------------------------
def json_timestamp(meta: dict) -> int | None:
    for key in ("photoTakenTime", "creationTime"):
        val = meta.get(key) or {}
        ts = str(val.get("timestamp", ""))
        if ts.isdigit() and int(ts) > 0:
            return int(ts)
    return None


def build_index(zips: list[Path]) -> dict:
    """Map (folder, title, dup#) -> epoch seconds, across ALL zip parts.

    A photo and its JSON can land in different zip parts, so the index has to
    be built from every zip before any media is placed.
    """
    idx = {"title": {}, "stem": {}}
    for zp in zips:
        try:
            with zipfile.ZipFile(zp) as zf:
                for info in zf.infolist():
                    if info.is_dir() or not info.filename.lower().endswith(".json"):
                        continue
                    try:
                        meta = json.loads(zf.read(info))
                    except (ValueError, OSError):
                        continue
                    title, ts = meta.get("title"), json_timestamp(meta)
                    if not title or ts is None:
                        continue
                    path = PurePosixPath(info.filename)
                    m = JSON_DUP_RE.search(path.name)
                    dup = int(m.group(1)) if m else 0
                    folder = str(path.parent)
                    idx["title"].setdefault((folder, title.lower(), dup), ts)
                    stem = os.path.splitext(title)[0].lower()
                    idx["stem"].setdefault((folder, stem, dup), ts)
        except zipfile.BadZipFile:
            print(f"  WARNING: {zp.name} is not a valid zip (incomplete download?)")
    return idx


def candidates(name: str):
    """Yield (title, dup#) guesses for a media filename."""
    yield name, 0
    m = DUP_RE.match(name)
    base, dup = (m["stem"] + (m["ext"] or ""), int(m["n"])) if m else (name, 0)
    if (base, dup) != (name, 0):
        yield base, dup
    stem, ext = os.path.splitext(base)
    if stem.endswith("-edited"):
        yield stem[: -len("-edited")] + ext, dup


def lookup(idx: dict, folder: str, name: str) -> int | None:
    cands = list(candidates(name))
    for base, dup in cands:
        ts = idx["title"].get((folder, base.lower(), dup))
        if ts is not None:
            return ts
    # Companion files (e.g. Live Photo .MP4 next to a .HEIC) share the stem
    for base, dup in cands:
        stem = os.path.splitext(base)[0].lower()
        ts = idx["stem"].get((folder, stem, dup))
        if ts is not None:
            return ts
    return None


# --------------------------------------------------------------------------
# Date fallbacks
# --------------------------------------------------------------------------
def exif_date(data: bytes) -> datetime | None:
    if Image is None:
        return None
    try:
        with Image.open(io.BytesIO(data)) as im:
            exif = im.getexif()
            val = exif.get_ifd(0x8769).get(36867) or exif.get(306)
            return datetime.strptime(str(val), "%Y:%m:%d %H:%M:%S") if val else None
    except Exception:
        return None


def resolve_date(zf, info, idx, utc: bool, stats: Counter) -> datetime | None:
    path = PurePosixPath(info.filename)
    ts = lookup(idx, str(path.parent), path.name)
    if ts is not None:
        stats["date from JSON"] += 1
        return datetime.fromtimestamp(ts, tz=UTC if utc else None)

    ext = path.suffix.lower()
    if Image is not None and ext in EXIF_EXTS and info.file_size <= MAX_EXIF_BYTES:
        with zf.open(info) as f:
            dt = exif_date(f.read())
        if dt:
            stats["date from EXIF"] += 1
            return dt

    m = NAME_DATE_RE.search(path.name)
    if m:
        try:
            dt = datetime(int(m[1]), int(m[2]), int(m[3]))
            stats["date from filename"] += 1
            return dt
        except ValueError:
            pass
    return None


# --------------------------------------------------------------------------
# Pass 2: write files
# --------------------------------------------------------------------------
def crc_of_file(path: Path) -> int:
    crc = 0
    with open(path, "rb") as f:
        while chunk := f.read(1024 * 1024):
            crc = zlib.crc32(chunk, crc)
    return crc


def target_folder(root: Path, dt: datetime | None, structure: str) -> Path:
    if dt is None:
        return root / "Unknown_Date"
    parts = [f"{dt:%Y}"]
    if structure in ("year-month", "year-month-day"):
        parts.append(f"{dt:%m}")
    if structure == "year-month-day":
        parts.append(f"{dt:%d}")
    return root.joinpath(*parts)


def place_file(zf, info, folder: Path, dt, dry_run: bool, seen: dict) -> str:
    """Copy one zip member into `folder`. Returns 'written' or 'duplicate'."""
    name = PurePosixPath(info.filename).name
    stem, ext = os.path.splitext(name)
    signature = (info.file_size, info.CRC)
    candidate, n = folder / name, 0

    while True:
        if candidate in seen:
            existing = seen[candidate]
        elif candidate.exists():
            existing = (candidate.stat().st_size, crc_of_file(candidate))
            seen[candidate] = existing
        else:
            break
        if existing == signature:
            return "duplicate"
        n += 1
        candidate = folder / f"{stem}_{n}{ext}"

    seen[candidate] = signature
    if dry_run:
        return "written"

    folder.mkdir(parents=True, exist_ok=True)
    with zf.open(info) as src, open(candidate, "wb") as dst:
        shutil.copyfileobj(src, dst, 1024 * 1024)
    if dt is not None:
        try:
            stamp = dt.timestamp()
            os.utime(candidate, (stamp, stamp))
        except (OverflowError, OSError, ValueError):
            pass
    return "written"


def main() -> int:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument(
        "source", type=Path, help="folder containing the Takeout .zip files"
    )
    ap.add_argument("dest", type=Path, help="where to put the organized photos")
    ap.add_argument(
        "--structure",
        choices=["year", "year-month", "year-month-day"],
        default="year-month",
        help="folder layout (default: year-month)",
    )
    ap.add_argument(
        "--utc",
        action="store_true",
        help="bucket dates by UTC instead of your local timezone",
    )
    ap.add_argument(
        "--dry-run", action="store_true", help="show what would happen, write nothing"
    )
    args = ap.parse_args()

    zips = sorted(args.source.glob("*.zip"))
    if not zips:
        print(f"No .zip files found in {args.source}")
        return 1

    print(f"Found {len(zips)} zip file(s). Reading metadata...")
    idx = build_index(zips)
    print(f"Indexed {len(idx['title'])} metadata entries.\n")

    stats: Counter = Counter()
    seen: dict = {}
    total = 0
    for zp in zips:
        print(f"Processing {zp.name}")
        try:
            zf = zipfile.ZipFile(zp)
        except zipfile.BadZipFile:
            continue
        with zf:
            for info in zf.infolist():
                if (
                    info.is_dir()
                    or PurePosixPath(info.filename).suffix.lower() in SKIP_EXT
                ):
                    continue
                total += 1
                dt = resolve_date(zf, info, idx, args.utc, stats)
                if dt is None:
                    stats["no date found"] += 1
                folder = target_folder(args.dest, dt, args.structure)
                try:
                    stats[place_file(zf, info, folder, dt, args.dry_run, seen)] += 1
                except OSError as e:
                    stats["errors"] += 1
                    print(f"  ERROR on {info.filename}: {e}")
                if total % 500 == 0:
                    print(f"  ...{total} files handled")

    label = "(dry run) " if args.dry_run else ""
    print(f"\n{label}Done. {total} media files examined:")
    for key in (
        "written",
        "duplicate",
        "date from JSON",
        "date from EXIF",
        "date from filename",
        "no date found",
        "errors",
    ):
        if stats[key]:
            print(f"  {key:>20}: {stats[key]}")
    if stats["no date found"]:
        print(f"\nFiles with no date are in {args.dest / 'Unknown_Date'}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
