# organize-photos

A dependency-free Go CLI for organizing Google Photos Takeout media by date.
It reads Takeout JSON sidecars first, then EXIF dates, then dates in filenames.
The `zip` command streams media directly from archive files without extracting
them, and `existing` organizes files that have already been extracted.

## Install

Build the executable from a checkout:

```sh
go build -o organize-photos .
```

On Windows, use an `.exe` suffix:

```sh
go build -o organize-photos.exe .
```

Place the resulting binary in a directory on your `PATH` to run it from
anywhere. The program uses only the Go standard library.

For example, on Linux or macOS:

```sh
go build -o "$HOME/.local/bin/organize-photos" .
```

## Usage

Organize all `.zip` files directly from a Takeout folder:

```sh
organize-photos zip ~/Downloads/takeout ~/Photos-Backup
organize-photos zip --dry-run ~/Downloads/takeout ~/Photos-Backup
```

Organize media in an already-extracted folder (moves files by default):

```sh
organize-photos existing ~/loose-photos /mnt/usbdrive/Photos-Backup
organize-photos existing --copy ~/loose-photos /mnt/usbdrive/Photos-Backup
```

Import media from an SD card or folder (copies files, leaving the source untouched):

```sh
organize-photos import /media/$USER/SDCARD/DCIM ~/Photos-Backup
organize-photos import --dry-run /media/$USER/SDCARD/DCIM ~/Photos-Backup
```

List mounted GVFS network-share paths (for example, shares shown in a desktop file manager):

```sh
organize-photos mounts
```

Use the printed path as the destination for `import` or `existing`, appending a
folder name if desired. The command checks `$XDG_RUNTIME_DIR/gvfs`, or
`/run/user/<user-id>/gvfs` when `XDG_RUNTIME_DIR` is unset.

The `existing` and `import` commands support:

- `--structure year|year-month|year-month-day` (default: `year-month`)
- `--utc` to bucket JSON dates using UTC instead of local time
- `--dry-run` to report actions without changing files

The `existing` command also supports `--remove-duplicates`, which removes a
source file when identical content is already present in the destination
(ignored with `--copy`). `import` always copies and leaves source files intact.
Existing-file duplicates are checked across the destination tree; ZIP duplicates
are checked in their target folder. Differing files with the same name are
preserved with a numeric suffix.

Files in ZIP archives with no usable date are placed in `Unknown_Date`. For
`existing` and `import`, the file's modified time is the final date fallback.
JSON and HTML files are not copied or moved. Dates are applied to the modified
time of files written from ZIP archives.

## Development

Run the tests with:

```sh
go test ./...
```
