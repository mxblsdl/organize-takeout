package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

func organizeExisting(source, dest string, opts options, stdout, stderr io.Writer) error {
	sourceInfo, err := os.Stat(source)
	if err != nil || !sourceInfo.IsDir() {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect source folder %q: %w", source, err)
		}
		return fmt.Errorf("source folder not found: %s", source)
	}

	fmt.Fprintln(stdout, "Reading JSON sidecars under source...")
	index := newDateIndex()
	var files []string
	err = filepath.WalkDir(source, func(filename string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !shouldSkip(filename) {
			files = append(files, filename)
		}
		if strings.EqualFold(filepath.Ext(filename), ".json") {
			data, err := os.ReadFile(filename)
			if err == nil {
				relative, relErr := filepath.Rel(source, filename)
				if relErr != nil {
					return relErr
				}
				addMetadata(index, filepath.ToSlash(relative), data)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("scan source folder: %w", err)
	}
	fmt.Fprintf(stdout, "Indexed %d metadata entries.\n", len(index.title))

	fmt.Fprintln(stdout, "Scanning existing destination for duplicates...")
	byName := make(map[string][]string)
	destCount := 0
	if _, err := os.Stat(dest); err == nil {
		err = filepath.WalkDir(dest, func(filename string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.IsDir() {
				byName[filepath.Base(filename)] = append(byName[filepath.Base(filename)], filename)
				destCount++
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("scan destination folder: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect destination folder %q: %w", dest, err)
	}
	fmt.Fprintf(stdout, "Found %d existing files in destination.\n\n", destCount)

	stats := newStats()
	for i, filename := range files {
		relative, err := filepath.Rel(source, filepath.Dir(filename))
		if err != nil {
			stats.add("errors")
			fmt.Fprintf(stderr, "  ERROR on %s: %v\n", filename, err)
			continue
		}
		info, err := os.Stat(filename)
		if err != nil {
			stats.add("errors")
			fmt.Fprintf(stderr, "  ERROR on %s: %v\n", filename, err)
			continue
		}
		date, sourceName, err := resolveExistingDate(filename, filepath.ToSlash(relative), index, info, opts.utc)
		if err != nil {
			stats.add("errors")
			fmt.Fprintf(stderr, "  ERROR resolving date on %s: %v\n", filename, err)
			continue
		}
		folder := targetFolder(dest, date, true, opts.structure)
		result, err := placeExistingFile(filename, folder, opts, byName)
		if err != nil {
			stats.add("errors")
			fmt.Fprintf(stderr, "  ERROR on %s: %v\n", filename, err)
		} else {
			stats.add(result)
		}
		if sourceName != "" {
			stats.add(sourceName)
		}
		if (i+1)%500 == 0 {
			fmt.Fprintf(stdout, "  ...%d files handled\n", i+1)
		}
	}

	if !opts.dryRun && !opts.copyFiles {
		if err := removeEmptySourceDirs(source); err != nil {
			fmt.Fprintf(stderr, "  WARNING: cleaning empty source folders: %v\n", err)
		}
	}
	label := ""
	if opts.dryRun {
		label = "(dry run) "
	}
	printSummary(stdout, label, len(files), stats, []string{
		"written", "duplicate", "date from JSON", "date from EXIF",
		"date from filename", "date from file's own mtime", "errors",
	})
	if stats.counts["date from file's own mtime"] > 0 {
		fmt.Fprintln(stdout, "\nNote: some files had no JSON/EXIF/filename date, so their own modified time was used -- double check these if that timestamp might be unreliable (e.g. set by extraction, not the original photo).")
	}
	return nil
}

func resolveExistingDate(filename, relativeDir string, index dateIndex, info os.FileInfo, utc bool) (time.Time, string, error) {
	if date, ok := metadataDate(index, relativeDir, filepath.Base(filename), utc); ok {
		return date, "date from JSON", nil
	}
	if isExifExtension(strings.ToLower(filepath.Ext(filename))) && info.Size() <= maxExifBytes {
		data, err := os.ReadFile(filename)
		if err != nil {
			return time.Time{}, "", err
		}
		if date, ok := exifDate(data); ok {
			return date, "date from EXIF", nil
		}
	}
	if date, ok := dateFromFilename(filepath.Base(filename)); ok {
		return date, "date from filename", nil
	}
	date := info.ModTime()
	if utc {
		date = date.UTC()
	} else {
		date = date.Local()
	}
	return date, "date from file's own mtime", nil
}

func placeExistingFile(source, folder string, opts options, byName map[string][]string) (string, error) {
	info, err := os.Stat(source)
	if err != nil {
		return "", err
	}
	name := filepath.Base(source)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	var sourceCRC uint32
	crcReady := false
	for suffix := 0; ; suffix++ {
		candidateName := name
		if suffix > 0 {
			candidateName = fmt.Sprintf("%s_%d%s", stem, suffix, ext)
		}
		candidate := filepath.Join(folder, candidateName)
		existingPaths := append([]string(nil), byName[candidateName]...)
		candidateFound := false
		for _, existing := range existingPaths {
			if filepath.Clean(existing) == filepath.Clean(candidate) {
				candidateFound = true
			}
		}
		if _, err := os.Stat(candidate); err == nil {
			if !candidateFound {
				existingPaths = append(existingPaths, candidate)
			}
			candidateFound = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		for _, existing := range existingPaths {
			existingInfo, err := os.Stat(existing)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return "", err
			}
			if existingInfo.Size() != info.Size() {
				continue
			}
			if !crcReady {
				sourceCRC, err = crcOfFile(source)
				if err != nil {
					return "", err
				}
				crcReady = true
			}
			existingCRC, err := crcOfFile(existing)
			if err != nil {
				return "", err
			}
			if existingCRC == sourceCRC {
				if opts.removeDuplicates && !opts.copyFiles && !opts.dryRun {
					if err := os.Remove(source); err != nil {
						return "", err
					}
				}
				return "duplicate", nil
			}
		}
		if len(byName[candidateName]) == 0 && !candidateFound {
			if opts.dryRun {
				byName[candidateName] = append(byName[candidateName], candidate)
				return "written", nil
			}
			if err := os.MkdirAll(folder, 0o755); err != nil {
				return "", err
			}
			if opts.copyFiles {
				err = copyFile(source, candidate, info)
			} else {
				err = moveFile(source, candidate, info)
			}
			if err != nil {
				return "", err
			}
			byName[candidateName] = append(byName[candidateName], candidate)
			return "written", nil
		}
	}
}

func copyFile(source, destination string, info os.FileInfo) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		return errors.Join(copyErr, closeErr)
	}
	if err := os.Chtimes(destination, info.ModTime(), info.ModTime()); err != nil {
		return err
	}
	return nil
}

func moveFile(source, destination string, info os.FileInfo) error {
	if err := os.Rename(source, destination); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if err := copyFile(source, destination, info); err != nil {
		return err
	}
	return os.Remove(source)
}

func removeEmptySourceDirs(source string) error {
	var directories []string
	err := filepath.WalkDir(source, func(filename string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && filename != source {
			directories = append(directories, filename)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(directories, func(i, j int) bool {
		return len(directories[i]) > len(directories[j])
	})
	for _, directory := range directories {
		if err := os.Remove(directory); err != nil && !errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.EEXIST) {
			return err
		}
	}
	return nil
}
