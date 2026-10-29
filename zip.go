package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type fileSignature struct {
	size uint64
	crc  uint32
}

func organizeZips(source, dest string, opts options, stdout, stderr io.Writer) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		return fmt.Errorf("read source folder %q: %w", source, err)
	}
	var zips []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".zip") {
			zips = append(zips, filepath.Join(source, entry.Name()))
		}
	}
	if len(zips) == 0 {
		return fmt.Errorf("no .zip files found in %s", source)
	}
	sort.Strings(zips)

	fmt.Fprintf(stdout, "Found %d zip file(s). Reading metadata...\n", len(zips))
	index := newDateIndex()
	for _, filename := range zips {
		reader, err := zip.OpenReader(filename)
		if err != nil {
			fmt.Fprintf(stderr, "  WARNING: %s is not a valid zip (incomplete download?)\n", filepath.Base(filename))
			continue
		}
		for _, member := range reader.File {
			if member.FileInfo().IsDir() || !strings.EqualFold(path.Ext(member.Name), ".json") {
				continue
			}
			file, err := member.Open()
			if err != nil {
				continue
			}
			data, readErr := io.ReadAll(file)
			closeErr := file.Close()
			if readErr == nil && closeErr == nil {
				addMetadata(index, member.Name, data)
			}
		}
		if err := reader.Close(); err != nil {
			fmt.Fprintf(stderr, "  WARNING: closing %s: %v\n", filepath.Base(filename), err)
		}
	}
	fmt.Fprintf(stdout, "Indexed %d metadata entries.\n\n", len(index.title))

	stats := newStats()
	seen := make(map[string]fileSignature)
	total := 0
	for _, filename := range zips {
		fmt.Fprintf(stdout, "Processing %s\n", filepath.Base(filename))
		reader, err := zip.OpenReader(filename)
		if err != nil {
			continue
		}
		for _, member := range reader.File {
			if member.FileInfo().IsDir() || shouldSkip(path.Base(member.Name)) {
				continue
			}
			total++
			date, hasDate, sourceName, err := resolveZipDate(reader, member, index, opts.utc)
			if err != nil {
				stats.add("errors")
				fmt.Fprintf(stderr, "  ERROR reading date on %s: %v\n", member.Name, err)
			}
			if hasDate {
				stats.add(sourceName)
			} else {
				stats.add("no date found")
			}
			folder := targetFolder(dest, date, hasDate, opts.structure)
			result, err := placeZipFile(member, folder, date, hasDate, opts.dryRun, seen)
			if err != nil {
				stats.add("errors")
				fmt.Fprintf(stderr, "  ERROR on %s: %v\n", member.Name, err)
			} else {
				stats.add(result)
			}
			if total%500 == 0 {
				fmt.Fprintf(stdout, "  ...%d files handled\n", total)
			}
		}
		if err := reader.Close(); err != nil {
			fmt.Fprintf(stderr, "  WARNING: closing %s: %v\n", filepath.Base(filename), err)
		}
	}

	label := ""
	if opts.dryRun {
		label = "(dry run) "
	}
	printSummary(stdout, label, total, stats, []string{
		"written", "duplicate", "date from JSON", "date from EXIF",
		"date from filename", "no date found", "errors",
	})
	if stats.counts["no date found"] > 0 {
		fmt.Fprintf(stdout, "\nFiles with no date are in %s\n", filepath.Join(dest, "Unknown_Date"))
	}
	return nil
}

func resolveZipDate(reader *zip.ReadCloser, member *zip.File, index dateIndex, utc bool) (time.Time, bool, string, error) {
	memberPath := path.Clean(member.Name)
	if date, ok := metadataDate(index, path.Dir(memberPath), path.Base(memberPath), utc); ok {
		return date, true, "date from JSON", nil
	}

	ext := strings.ToLower(path.Ext(memberPath))
	if isExifExtension(ext) && member.UncompressedSize64 <= maxExifBytes {
		file, err := member.Open()
		if err != nil {
			return time.Time{}, false, "", err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxExifBytes))
		closeErr := file.Close()
		if readErr != nil {
			return time.Time{}, false, "", readErr
		}
		if closeErr != nil {
			return time.Time{}, false, "", closeErr
		}
		if date, ok := exifDate(data); ok {
			return date, true, "date from EXIF", nil
		}
	}
	if date, ok := dateFromFilename(path.Base(memberPath)); ok {
		return date, true, "date from filename", nil
	}
	return time.Time{}, false, "", nil
}

func placeZipFile(member *zip.File, folder string, date time.Time, hasDate, dryRun bool, seen map[string]fileSignature) (string, error) {
	name := path.Base(member.Name)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	signature := fileSignature{size: member.UncompressedSize64, crc: member.CRC32}
	var candidate string
	for suffix := 0; ; suffix++ {
		candidateName := name
		if suffix > 0 {
			candidateName = fmt.Sprintf("%s_%d%s", stem, suffix, ext)
		}
		candidate = filepath.Join(folder, candidateName)
		existing, found, err := signatureAt(candidate, seen)
		if err != nil {
			return "", err
		}
		if !found {
			break
		}
		if existing == signature {
			return "duplicate", nil
		}
	}
	if dryRun {
		seen[candidate] = signature
		return "written", nil
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return "", err
	}
	src, err := member.Open()
	if err != nil {
		return "", err
	}
	dst, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		src.Close()
		return "", err
	}
	_, copyErr := io.Copy(dst, src)
	srcCloseErr := src.Close()
	dstCloseErr := dst.Close()
	if copyErr != nil || srcCloseErr != nil || dstCloseErr != nil {
		_ = os.Remove(candidate)
		return "", errors.Join(copyErr, srcCloseErr, dstCloseErr)
	}
	if hasDate {
		if err := os.Chtimes(candidate, date, date); err != nil {
			return "", err
		}
	}
	seen[candidate] = signature
	return "written", nil
}

func signatureAt(filename string, seen map[string]fileSignature) (fileSignature, bool, error) {
	if signature, ok := seen[filename]; ok {
		return signature, true, nil
	}
	info, err := os.Stat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return fileSignature{}, false, nil
	}
	if err != nil {
		return fileSignature{}, false, err
	}
	crc, err := crcOfFile(filename)
	if err != nil {
		return fileSignature{}, false, err
	}
	signature := fileSignature{size: uint64(info.Size()), crc: crc}
	seen[filename] = signature
	return signature, true, nil
}
