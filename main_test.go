package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDateIndexDuplicateAndEditedCandidates(t *testing.T) {
	index := newDateIndex()
	addMetadata(index, "photos/IMG_1000.jpg(2).json", []byte(`{"title":"IMG_1000.jpg","photoTakenTime":{"timestamp":"1600000000"}}`))
	addMetadata(index, "photos/IMG_1000.jpg.json", []byte(`{"title":"IMG_1000.jpg","photoTakenTime":{"timestamp":"1600000001"}}`))

	for name, want := range map[string]int64{
		"IMG_1000(2).jpg":     1600000000,
		"IMG_1000-edited.jpg": 1600000001,
	} {
		if ts, ok := index.lookup("photos", name); !ok || ts != want {
			t.Fatalf("lookup(%q) = %d, %t; want %d and true", name, ts, ok, want)
		}
	}
}

func TestDateFromFilename(t *testing.T) {
	date, ok := dateFromFilename("photo_2020-02-29_edited.jpg")
	if !ok || date.Format("2006-01-02") != "2020-02-29" {
		t.Fatalf("dateFromFilename returned %v, %t", date, ok)
	}
	if _, ok := dateFromFilename("photo_2021-02-29.jpg"); ok {
		t.Fatal("invalid calendar date should not match")
	}
}

func TestExifDateFromTIFF(t *testing.T) {
	const rootOffset = 8
	const exifOffset = 26
	const dateOffset = 44
	dateValue := []byte("2021:08:09 10:11:12\x00")
	data := make([]byte, dateOffset+len(dateValue))
	copy(data[:2], "II")
	binary.LittleEndian.PutUint16(data[2:4], 42)
	binary.LittleEndian.PutUint32(data[4:8], rootOffset)
	binary.LittleEndian.PutUint16(data[rootOffset:rootOffset+2], 1)
	rootEntry := data[rootOffset+2 : rootOffset+14]
	binary.LittleEndian.PutUint16(rootEntry[:2], 0x8769)
	binary.LittleEndian.PutUint16(rootEntry[2:4], 4)
	binary.LittleEndian.PutUint32(rootEntry[4:8], 1)
	binary.LittleEndian.PutUint32(rootEntry[8:12], exifOffset)
	binary.LittleEndian.PutUint16(data[exifOffset:exifOffset+2], 1)
	exifEntry := data[exifOffset+2 : exifOffset+14]
	binary.LittleEndian.PutUint16(exifEntry[:2], 0x9003)
	binary.LittleEndian.PutUint16(exifEntry[2:4], 2)
	binary.LittleEndian.PutUint32(exifEntry[4:8], uint32(len(dateValue)))
	binary.LittleEndian.PutUint32(exifEntry[8:12], dateOffset)
	copy(data[dateOffset:], dateValue)

	date, ok := exifDate(data)
	if !ok || date.Format("2006-01-02 15:04:05") != "2021-08-09 10:11:12" {
		t.Fatalf("exifDate returned %v, %t", date, ok)
	}
}

func TestOrganizeZipsIndexesSidecarsAcrossArchivesAndDeduplicates(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "takeout")
	dest := filepath.Join(root, "organized")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}

	makeZip(t, filepath.Join(source, "01-media.zip"), map[string][]byte{
		"Takeout/Google Photos/Album/photo.jpg":       []byte("first image"),
		"Takeout/Google Photos/Album/no-date.bin":     []byte("unknown"),
		"Takeout/Google Photos/Other Album/photo.jpg": []byte("different image"),
	})
	makeZip(t, filepath.Join(source, "02-sidecars.zip"), map[string][]byte{
		"Takeout/Google Photos/Album/photo.jpg.json":       []byte(`{"title":"photo.jpg","photoTakenTime":{"timestamp":"1598918400"}}`),
		"Takeout/Google Photos/Other Album/photo.jpg.json": []byte(`{"title":"photo.jpg","photoTakenTime":{"timestamp":"1598918400"}}`),
	})
	makeZip(t, filepath.Join(source, "03-duplicates.zip"), map[string][]byte{
		"Takeout/Google Photos/Album/photo.jpg": []byte("first image"),
	})

	var stdout, stderr bytes.Buffer
	if err := organizeZips(source, dest, options{structure: "year-month", utc: true}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dest, "2020", "09", "photo.jpg")); err != nil {
		t.Fatalf("dated file not found: %v", err)
	}
	collision, err := os.ReadFile(filepath.Join(dest, "2020", "09", "photo_1.jpg"))
	if err != nil || string(collision) != "different image" {
		t.Fatalf("different same-named photo should be retained with suffix: %q, %v", collision, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "Unknown_Date", "no-date.bin")); err != nil {
		t.Fatalf("undated file not found: %v", err)
	}
	if !strings.Contains(stdout.String(), "duplicate:") || !strings.Contains(stdout.String(), "date from JSON:") {
		t.Fatalf("missing summary counts: %s", stdout.String())
	}
}

func TestOrganizeExistingMovesAndCopies(t *testing.T) {
	t.Run("move", func(t *testing.T) {
		root := t.TempDir()
		source := filepath.Join(root, "source")
		dest := filepath.Join(root, "dest")
		if err := os.Mkdir(source, 0o755); err != nil {
			t.Fatal(err)
		}
		sourceFile := filepath.Join(source, "IMG_20200615.jpg")
		if err := os.WriteFile(sourceFile, []byte("photo"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sourceFile+".json", []byte(`{"title":"IMG_20200615.jpg","photoTakenTime":{"timestamp":"1598918400"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if err := organizeExisting(source, dest, options{structure: "year-month", utc: true}, &stdout, &stderr); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(sourceFile); !os.IsNotExist(err) {
			t.Fatal("source file should be moved")
		}
		if _, err := os.Stat(filepath.Join(dest, "2020", "09", "IMG_20200615.jpg")); err != nil {
			t.Fatalf("moved file not found: %v", err)
		}
	})

	t.Run("copy", func(t *testing.T) {
		root := t.TempDir()
		source := filepath.Join(root, "source")
		dest := filepath.Join(root, "dest")
		if err := os.Mkdir(source, 0o755); err != nil {
			t.Fatal(err)
		}
		sourceFile := filepath.Join(source, "IMG_20200615.jpg")
		if err := os.WriteFile(sourceFile, []byte("photo"), 0o644); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if err := organizeExisting(source, dest, options{structure: "year-month", copyFiles: true}, &stdout, &stderr); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(sourceFile); err != nil {
			t.Fatal("copy must preserve source file")
		}
		if _, err := os.Stat(filepath.Join(dest, "2020", "06", "IMG_20200615.jpg")); err != nil {
			t.Fatalf("copied file not found: %v", err)
		}
	})

	t.Run("dry run", func(t *testing.T) {
		root := t.TempDir()
		source := filepath.Join(root, "source")
		dest := filepath.Join(root, "dest")
		if err := os.Mkdir(source, 0o755); err != nil {
			t.Fatal(err)
		}
		sourceFile := filepath.Join(source, "IMG_20200615.jpg")
		if err := os.WriteFile(sourceFile, []byte("photo"), 0o644); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if err := organizeExisting(source, dest, options{structure: "year-month", dryRun: true}, &stdout, &stderr); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(sourceFile); err != nil {
			t.Fatal("dry run must preserve source file")
		}
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Fatal("dry run must not create destination")
		}
	})
}

func TestOrganizeExistingRemovesDestinationDuplicates(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	dest := filepath.Join(root, "dest")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(dest, "somewhere", "IMG_20200615.jpg")
	if err := os.MkdirAll(filepath.Dir(existing), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("same content"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceFile := filepath.Join(source, "IMG_20200615.jpg")
	if err := os.WriteFile(sourceFile, []byte("same content"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := organizeExisting(source, dest, options{
		structure: "year-month", removeDuplicates: true,
	}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sourceFile); !os.IsNotExist(err) {
		t.Fatal("duplicate source file should be removed")
	}
	if !strings.Contains(stdout.String(), "duplicate:") {
		t.Fatalf("duplicate count missing from output: %s", stdout.String())
	}
}

func makeZip(t *testing.T, filename string, entries map[string][]byte) {
	t.Helper()
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for name, data := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRunHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(help) exit code = %d", code)
	}
	if !strings.Contains(stdout.String(), "organize-photos <command>") {
		t.Fatalf("unexpected help: %s", stdout.String())
	}
}

func TestRunImportCopiesFilesIntoYearMonthFolders(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "sd-card")
	dest := filepath.Join(root, "photos")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	sourceFile := filepath.Join(source, "IMG_20200615.jpg")
	if err := os.WriteFile(sourceFile, []byte("photo"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"import", source, dest}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(import) exit code = %d; stderr: %s", code, stderr.String())
	}
	if _, err := os.Stat(sourceFile); err != nil {
		t.Fatalf("import should leave the source file untouched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "2020", "06", "IMG_20200615.jpg")); err != nil {
		t.Fatalf("imported file not found in year/month folder: %v", err)
	}
}

func TestFindGVFSMounts(t *testing.T) {
	runtimeDir := t.TempDir()
	gvfsDir := filepath.Join(runtimeDir, "gvfs")
	for _, name := range []string{"smb-share:server=nas,share=photos", "smb-share:server=nas,share=backup"} {
		if err := os.MkdirAll(filepath.Join(gvfsDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(gvfsDir, "not-a-mount"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}

	mounts, err := findGVFSMounts(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(gvfsDir, "smb-share:server=nas,share=backup"),
		filepath.Join(gvfsDir, "smb-share:server=nas,share=photos"),
	}
	if strings.Join(mounts, "\n") != strings.Join(want, "\n") {
		t.Fatalf("findGVFSMounts() = %v, want %v", mounts, want)
	}
}

func TestRunMountsPrintsGVFSPaths(t *testing.T) {
	runtimeDir := t.TempDir()
	mountPath := filepath.Join(runtimeDir, "gvfs", "smb-share:server=nas,share=photos")
	if err := os.MkdirAll(mountPath, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"mounts"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(mounts) exit code = %d; stderr: %s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != mountPath {
		t.Fatalf("run(mounts) output = %q, want %q", stdout.String(), mountPath)
	}
}
