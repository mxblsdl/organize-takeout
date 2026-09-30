#!/usr/bin/env python3
"""
organize_existing.py - Move an already-unzipped folder of photos into the same
year/month structure produced by organize_takeout.py.

What it does
  * Walks SOURCE recursively for media files.
  * Uses JSON sidecar files next to each photo to work out its "taken" date
    (same Google Takeout json format), then EXIF, then a date in the
    filename, then finally the file's own modified time.
  * Moves each file into DEST/<year>/<month>/ (matching organize_takeout.py's
    default layout -- use --structure to match if you used a different one).
  * Skips files that already exist in DEST with the same content (checked
    against your whole destination tree, not just the folder it's landing in),
    so it's safe to run against a folder that partly overlaps what's already
    there.
  * Originals are moved (deleted from SOURCE) unless you pass --copy.

Usage
  python organize_existing.py ~/loose-photos /mnt/usbdrive/Photos-Backup
  python organize_existing.py ~/loose-photos /mnt/usbdrive/Photos-Backup --dry-run
  python organize_existing.py ~/loose-photos /mnt/usbdrive/Photos-Backup --copy --structure year
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import sys
import zlib
from collections import Counter
from datetime import UTC, datetime
from pathlib import Path

try:
    from PIL import Image  # optional, used only as an EXIF fallback
except ImportError:
    Image = None

SKIP_EXT = {".json", ".html"}
EXIF_EXTS = {".jpg", ".jpeg", ".tif", ".tiff"}
MAX_EXIF_BYTES = 100 * 1024 * 1024
DUP_RE = re.compile(r"^(?P<stem>.*)\((?P<n>\d+)\)(?P<ext>\.[^.]*)?$")
JSON_DUP_RE = re.compile(r"\((\d+)\)\.json$", re.IGNORECASE)
NAME_DATE_RE = re.compile(
    r"(?<!\d)(19[89]\d|20[0-4]\d)[-_.]?(0[1-9]|1[0-2])[-_.]?(0[1-9]|[12]\d|3[01])(?!\d)"
)


# --------------------------------------------------------------------------
# Pass 1: build a date index from every JSON sidecar under SOURCE
# --------------------------------------------------------------------------
def json_timestamp(meta: dict) -> int | None:
    for key in ("photoTakenTime", "creationTime"):
        val = meta.get(key) or {}
        ts = str(val.get("timestamp", ""))
        if ts.isdigit() and int(ts) > 0:
            return int(ts)
    return None


def build_index(source: Path) -> dict:
    idx = {"title": {}, "stem": {}}
    for dirpath, _, filenames in os.walk(source):
        rel_dir = str(Path(dirpath).relative_to(source))
        for fname in filenames:
            if not fname.lower().endswith(".json"):
                continue
            try:
                meta = json.loads((Path(dirpath) / fname).read_text(encoding="utf-8"))
            except (ValueError, OSError, UnicodeDecodeError):
                continue
            title, ts = meta.get("title"), json_timestamp(meta)
            if not title or ts is None:
                continue
            m = JSON_DUP_RE.search(fname)
            dup = int(m.group(1)) if m else 0
            idx["title"].setdefault((rel_dir, title.lower(), dup), ts)
            stem = os.path.splitext(title)[0].lower()
            idx["stem"].setdefault((rel_dir, stem, dup), ts)
    return idx


def candidates(name: str):
    yield name, 0
    m = DUP_RE.match(name)
    base, dup = (m["stem"] + (m["ext"] or ""), int(m["n"])) if m else (name, 0)
    if (base, dup) != (name, 0):
        yield base, dup
    stem, ext = os.path.splitext(base)
    if stem.endswith("-edited"):
        yield stem[: -len("-edited")] + ext, dup


def lookup(idx: dict, rel_dir: str, name: str) -> int | None:
    cands = list(candidates(name))
    for base, dup in cands:
        ts = idx["title"].get((rel_dir, base.lower(), dup))
        if ts is not None:
            return ts
    for base, dup in cands:
        stem = os.path.splitext(base)[0].lower()
        ts = idx["stem"].get((rel_dir, stem, dup))
        if ts is not None:
            return ts
    return None


# --------------------------------------------------------------------------
# Date fallbacks
# --------------------------------------------------------------------------
def exif_date(path: Path) -> datetime | None:
    if Image is None:
        return None
    try:
        with Image.open(path) as im:
            exif = im.getexif()
            val = exif.get_ifd(0x8769).get(36867) or exif.get(306)
            return datetime.strptime(str(val), "%Y:%m:%d %H:%M:%S") if val else None
    except Exception:
        return None


def resolve_date(
    path: Path, rel_dir: str, idx: dict, utc: bool, stats: Counter
) -> tuple[datetime | None, bool]:
    """Returns (date, is_fallback_mtime)."""
    ts = lookup(idx, rel_dir, path.name)
    if ts is not None:
        stats["date from JSON"] += 1
        return datetime.fromtimestamp(ts, tz=UTC if utc else None), False

    if (
        Image is not None
        and path.suffix.lower() in EXIF_EXTS
        and path.stat().st_size <= MAX_EXIF_BYTES
    ):
        dt = exif_date(path)
        if dt:
            stats["date from EXIF"] += 1
            return dt, False

    m = NAME_DATE_RE.search(path.name)
    if m:
        try:
            dt = datetime(int(m[1]), int(m[2]), int(m[3]))
            stats["date from filename"] += 1
            return dt, False
        except ValueError:
            pass

    stats["date from file's own mtime"] += 1
    dt = datetime.fromtimestamp(path.stat().st_mtime, tz=UTC if utc else None)
    return dt, True


# --------------------------------------------------------------------------
# Destination handling
# --------------------------------------------------------------------------
def crc_of_file(path: Path) -> int:
    crc = 0
    with open(path, "rb") as f:
        while chunk := f.read(1024 * 1024):
            crc = zlib.crc32(chunk, crc)
    return crc


def index_existing_dest(dest: Path) -> dict:
    """Pre-scan DEST so duplicates are caught even if they're not in the
    exact folder a file would land in this run."""
    seen: dict[str, list[Path]] = {}
    if not dest.exists():
        return seen
    for dirpath, _, filenames in os.walk(dest):
        for fname in filenames:
            seen.setdefault(fname, []).append(Path(dirpath) / fname)
    return seen


def target_folder(root: Path, dt: datetime | None, structure: str) -> Path:
    if dt is None:
        return root / "Unknown_Date"
    parts = [f"{dt:%Y}"]
    if structure in ("year-month", "year-month-day"):
        parts.append(f"{dt:%m}")
    if structure == "year-month-day":
        parts.append(f"{dt:%d}")
    return root.joinpath(*parts)


def place_file(
    path: Path,
    folder: Path,
    dry_run: bool,
    copy: bool,
    by_name: dict,
    remove_duplicates: bool,
) -> str:
    name = path.name
    stem, ext = os.path.splitext(name)
    size = path.stat().st_size
    my_crc = None  # computed lazily, only if a same-named file is found

    candidate, n = folder / name, 0
    while candidate.name in by_name or candidate.exists():
        existing_paths = by_name.get(candidate.name, [])
        if candidate.exists() and candidate not in existing_paths:
            existing_paths = existing_paths + [candidate]
        match = None
        for existing in existing_paths:
            if not existing.exists() or existing.stat().st_size != size:
                continue
            if my_crc is None:
                my_crc = crc_of_file(path)
            if crc_of_file(existing) == my_crc:
                match = existing
                break
        if match is not None:
            if remove_duplicates and not copy and not dry_run:
                path.unlink()
            return "duplicate"
        n += 1
        candidate = folder / f"{stem}_{n}{ext}"

    if dry_run:
        by_name.setdefault(candidate.name, []).append(candidate)
        return "written"

    folder.mkdir(parents=True, exist_ok=True)
    if copy:
        shutil.copy2(path, candidate)
    else:
        shutil.move(str(path), str(candidate))
    by_name.setdefault(candidate.name, []).append(candidate)
    return "written"


def main() -> int:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument(
        "source", type=Path, help="folder of already-unzipped photos to scan"
    )
    ap.add_argument("dest", type=Path, help="the organized year/month backup folder")
    ap.add_argument(
        "--structure",
        choices=["year", "year-month", "year-month-day"],
        default="year-month",
        help="must match how DEST is already organized (default: year-month)",
    )
    ap.add_argument(
        "--utc",
        action="store_true",
        help="bucket dates by UTC instead of local timezone",
    )
    ap.add_argument(
        "--copy",
        action="store_true",
        help="copy instead of move, leaving SOURCE untouched",
    )
    ap.add_argument(
        "--remove-duplicates",
        action="store_true",
        help="delete a source file from SOURCE when it's found to already exist in DEST "
        "(ignored with --copy)",
    )
    ap.add_argument(
        "--dry-run", action="store_true", help="show what would happen, change nothing"
    )
    args = ap.parse_args()

    if not args.source.is_dir():
        print(f"Source folder not found: {args.source}")
        return 1

    print("Reading JSON sidecars under source...")
    idx = build_index(args.source)
    print(f"Indexed {len(idx['title'])} metadata entries.")

    print("Scanning existing destination for duplicates...")
    by_name = index_existing_dest(args.dest)
    print(
        f"Found {sum(len(v) for v in by_name.values())} existing files in destination.\n"
    )

    stats: Counter = Counter()
    total = 0
    for dirpath, _, filenames in os.walk(args.source):
        rel_dir = str(Path(dirpath).relative_to(args.source))
        for fname in sorted(filenames):
            if Path(fname).suffix.lower() in SKIP_EXT:
                continue
            path = Path(dirpath) / fname
            total += 1
            dt, is_fallback = resolve_date(path, rel_dir, idx, args.utc, stats)
            folder = target_folder(args.dest, dt, args.structure)
            try:
                stats[
                    place_file(
                        path,
                        folder,
                        args.dry_run,
                        args.copy,
                        by_name,
                        args.remove_duplicates,
                    )
                ] += 1
            except OSError as e:
                stats["errors"] += 1
                print(f"  ERROR on {path}: {e}")
            if total % 500 == 0:
                print(f"  ...{total} files handled")

    # Clean up now-empty source directories after a real move
    if not args.dry_run and not args.copy:
        for dirpath, dirnames, filenames in os.walk(args.source, topdown=False):
            d = Path(dirpath)
            if d != args.source and not filenames and not dirnames:
                try:
                    d.rmdir()
                except OSError:
                    pass

    label = "(dry run) " if args.dry_run else ""
    print(f"\n{label}Done. {total} files examined:")
    for key in (
        "written",
        "duplicate",
        "date from JSON",
        "date from EXIF",
        "date from filename",
        "date from file's own mtime",
        "errors",
    ):
        if stats[key]:
            print(f"  {key:>28}: {stats[key]}")
    if stats["date from file's own mtime"]:
        print(
            "\nNote: some files had no JSON/EXIF/filename date, so their own "
            "modified time was used -- double check these if that timestamp "
            "might be unreliable (e.g. set by extraction, not the original photo)."
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())
