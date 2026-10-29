package main

import (
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	skipJSONExt  = ".json"
	skipHTMLExt  = ".html"
	maxExifBytes = 100 * 1024 * 1024
)

type options struct {
	structure        string
	utc              bool
	dryRun           bool
	copyFiles        bool
	removeDuplicates bool
}

type dateKey struct {
	folder string
	name   string
	dup    int
}

type dateIndex struct {
	title map[dateKey]int64
	stem  map[dateKey]int64
}

type filenameCandidate struct {
	name string
	dup  int
}

type stats struct {
	counts map[string]int
}

var (
	duplicateSuffixRE = regexp.MustCompile(`^(.*)\(([0-9]+)\)(\.[^.]+)?$`)
	jsonDuplicateRE   = regexp.MustCompile(`(?i)\(([0-9]+)\)\.json$`)
	nameDateRE        = regexp.MustCompile(`(?:^|[^\d])(19[89]\d|20[0-4]\d)[-_.]?(0[1-9]|1[0-2])[-_.]?(0[1-9]|[12]\d|3[01])(?:[^\d]|$)`)
)

func newDateIndex() dateIndex {
	return dateIndex{title: make(map[dateKey]int64), stem: make(map[dateKey]int64)}
}

func newStats() *stats {
	return &stats{counts: make(map[string]int)}
}

func (s *stats) add(key string) {
	s.counts[key]++
}

func validStructure(structure string) bool {
	return structure == "year" || structure == "year-month" || structure == "year-month-day"
}

func shouldSkip(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == skipJSONExt || ext == skipHTMLExt
}

func timestampFromMetadata(data []byte) (string, int64, bool) {
	var metadata struct {
		Title          string `json:"title"`
		PhotoTakenTime struct {
			Timestamp json.RawMessage `json:"timestamp"`
		} `json:"photoTakenTime"`
		CreationTime struct {
			Timestamp json.RawMessage `json:"timestamp"`
		} `json:"creationTime"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil || metadata.Title == "" {
		return "", 0, false
	}

	for _, raw := range []json.RawMessage{metadata.PhotoTakenTime.Timestamp, metadata.CreationTime.Timestamp} {
		value := strings.Trim(string(raw), `"`)
		ts, err := strconv.ParseInt(value, 10, 64)
		if err == nil && ts > 0 {
			return metadata.Title, ts, true
		}
	}
	return "", 0, false
}

func addMetadata(index dateIndex, filename string, data []byte) {
	title, ts, ok := timestampFromMetadata(data)
	if !ok {
		return
	}
	name := path.Base(filename)
	dup := 0
	if match := jsonDuplicateRE.FindStringSubmatch(name); match != nil {
		dup, _ = strconv.Atoi(match[1])
	}
	folder := path.Dir(filename)
	key := dateKey{folder: folder, name: strings.ToLower(title), dup: dup}
	if _, exists := index.title[key]; !exists {
		index.title[key] = ts
	}
	stem := strings.TrimSuffix(title, path.Ext(title))
	stemKey := dateKey{folder: folder, name: strings.ToLower(stem), dup: dup}
	if _, exists := index.stem[stemKey]; !exists {
		index.stem[stemKey] = ts
	}
}

func filenameCandidates(name string) []filenameCandidate {
	candidates := []filenameCandidate{{name: name}}
	base, dup := name, 0
	if match := duplicateSuffixRE.FindStringSubmatch(name); match != nil {
		dup, _ = strconv.Atoi(match[2])
		base = match[1] + match[3]
		if base != name {
			candidates = append(candidates, filenameCandidate{name: base, dup: dup})
		}
	}
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if strings.HasSuffix(stem, "-edited") {
		candidates = append(candidates, filenameCandidate{name: strings.TrimSuffix(stem, "-edited") + ext, dup: dup})
	}
	return candidates
}

func (index dateIndex) lookup(folder, name string) (int64, bool) {
	candidates := filenameCandidates(name)
	for _, candidate := range candidates {
		key := dateKey{folder: folder, name: strings.ToLower(candidate.name), dup: candidate.dup}
		if ts, ok := index.title[key]; ok {
			return ts, true
		}
	}
	for _, candidate := range candidates {
		stem := strings.TrimSuffix(candidate.name, path.Ext(candidate.name))
		key := dateKey{folder: folder, name: strings.ToLower(stem), dup: candidate.dup}
		if ts, ok := index.stem[key]; ok {
			return ts, true
		}
	}
	return 0, false
}

func metadataDate(index dateIndex, folder, name string, utc bool) (time.Time, bool) {
	ts, ok := index.lookup(folder, name)
	if !ok {
		return time.Time{}, false
	}
	location := time.Local
	if utc {
		location = time.UTC
	}
	return time.Unix(ts, 0).In(location), true
}

func dateFromFilename(name string) (time.Time, bool) {
	match := nameDateRE.FindStringSubmatch(name)
	if match == nil {
		return time.Time{}, false
	}
	year, _ := strconv.Atoi(match[1])
	month, _ := strconv.Atoi(match[2])
	day, _ := strconv.Atoi(match[3])
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.Local)
	if date.Year() != year || int(date.Month()) != month || date.Day() != day {
		return time.Time{}, false
	}
	return date, true
}

func targetFolder(root string, date time.Time, hasDate bool, structure string) string {
	if !hasDate {
		return filepath.Join(root, "Unknown_Date")
	}
	parts := []string{date.Format("2006")}
	if structure == "year-month" || structure == "year-month-day" {
		parts = append(parts, date.Format("01"))
	}
	if structure == "year-month-day" {
		parts = append(parts, date.Format("02"))
	}
	return filepath.Join(append([]string{root}, parts...)...)
}

func crcOfFile(filename string) (uint32, error) {
	file, err := os.Open(filename)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	hash := crc32.NewIEEE()
	if _, err := io.Copy(hash, file); err != nil {
		return 0, err
	}
	return hash.Sum32(), nil
}

func printSummary(w io.Writer, label string, total int, stats *stats, keys []string) {
	if label != "" {
		fmt.Fprintf(w, "\n%s", label)
	} else {
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "Done. %d files examined:\n", total)
	for _, key := range keys {
		if stats.counts[key] > 0 {
			fmt.Fprintf(w, "  %28s: %d\n", key, stats.counts[key])
		}
	}
}
