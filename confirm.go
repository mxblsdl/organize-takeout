package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

func missingFolders(folders []string) ([]string, error) {
	unique := make(map[string]struct{}, len(folders))
	var missing []string
	for _, folder := range folders {
		if _, ok := unique[folder]; ok {
			continue
		}
		unique[folder] = struct{}{}

		info, err := os.Stat(folder)
		if errors.Is(err, os.ErrNotExist) {
			missing = append(missing, folder)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect destination folder %q: %w", folder, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("destination path %q is not a directory", folder)
		}
	}
	sort.Strings(missing)
	return missing, nil
}

func confirmMissingFolders(input io.Reader, output io.Writer, folders []string) (bool, error) {
	fmt.Fprintln(output, "These destination folders do not exist and will be created:")
	const maxShown = 3
	for i, folder := range folders {
		if i == maxShown {
			fmt.Fprintf(output, "  ...and %d more\n", len(folders)-maxShown)
			break
		}
		fmt.Fprintf(output, "  %s\n", folder)
	}
	fmt.Fprint(output, "Continue? [y/N]: ")

	response, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read confirmation: %w", err)
	}
	answer := strings.TrimSpace(response)
	if strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes") {
		return true, nil
	}
	fmt.Fprintln(output, "Cancelled; no files were written or moved.")
	return false, nil
}
