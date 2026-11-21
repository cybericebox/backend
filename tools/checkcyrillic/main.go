// Command checkcyrillic keeps the "English only" rule of the repository: a
// Cyrillic character in a non-test .go or .sql file is a failure unless the
// file belongs to a module whose job is to hold user-visible Ukrainian text
// (internal/model/usertext, the notification default modules) or is a released
// migration. Tests, fixtures and test data are not scanned.
//
// Usage: go run ./tools/checkcyrillic [root]   (default: the current directory)
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// allowedDirs are the text modules, as slash-separated paths below the root.
var allowedDirs = []string{
	"internal/model/usertext",
	"internal/model/notification/emaildefaults",
	"internal/model/notification/inappdefaults",
}

// skippedDirNames are directories that are never scanned, wherever they are.
var skippedDirNames = map[string]bool{
	".git": true, ".worktrees": true, "node_modules": true, "vendor": true,
	"testdata": true, "migrations": true,
}

// scanned reports whether the file (a slash-separated path below the root) is checked.
func scanned(rel string) bool {
	if !strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, ".sql") {
		return false
	}
	if strings.HasSuffix(rel, "_test.go") {
		return false
	}
	for _, d := range allowedDirs {
		if rel == d || strings.HasPrefix(rel, d+"/") {
			return false
		}
	}
	return true
}

// findings returns the 1-based line numbers of src that contain a Cyrillic character.
func findings(src []byte) []int {
	var lines []int
	sc := bufio.NewScanner(bytes.NewReader(src))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for n := 1; sc.Scan(); n++ {
		for _, r := range sc.Text() {
			if unicode.Is(unicode.Cyrillic, r) {
				lines = append(lines, n)
				break
			}
		}
	}
	return lines
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skippedDirNames[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !scanned(rel) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, n := range findings(data) {
			found = append(found, fmt.Sprintf("%s:%d", rel, n))
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "checkcyrillic:", err)
		os.Exit(2)
	}
	if len(found) > 0 {
		fmt.Fprintln(os.Stderr, "Cyrillic text outside the text modules (move user-visible text to internal/model/usertext, translate the rest to English):")
		for _, f := range found {
			fmt.Fprintln(os.Stderr, "  "+f)
		}
		os.Exit(1)
	}
}
