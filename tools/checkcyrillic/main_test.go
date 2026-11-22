package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestScanned(t *testing.T) {
	cases := map[string]bool{
		"internal/a/b.go":                                  true,
		"internal/a/q.sql":                                 true,
		"internal/a/b_test.go":                             false,
		"internal/a/readme.md":                             false,
		"internal/model/usertext/uk_mail.go":               false,
		"internal/model/notification/emaildefaults/uk.go":  false,
		"internal/model/notification/inappdefaults/x.go":   false,
		"internal/model/notification/emaildefaultsx/uk.go": true,
		"internal/model/usertextual/a.go":                  true,
	}
	for rel, want := range cases {
		if got := scanned(rel); got != want {
			t.Errorf("scanned(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestFindings(t *testing.T) {
	src := []byte("package x\n// plain\nvar a = \"\u0442\u0435\u043a\u0441\u0442\"\nvar b = 1\nvar c = \"\u0406\"\n")
	if got, want := findings(src), []int{3, 5}; !reflect.DeepEqual(got, want) {
		t.Errorf("findings = %v, want %v", got, want)
	}
	if got := findings([]byte("only latin \u00e9 \u2019\n")); len(got) != 0 {
		t.Errorf("non-Cyrillic text flagged: %v", got)
	}
}

func TestWalkSkipsAllowedAndTests(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	text := "var a = \"\u0442\"\n"
	write("internal/model/usertext/uk.go", text)
	write("internal/a/a_test.go", text)
	write("internal/a/migrations/1.sql", text)
	write("internal/a/testdata/x.go", text)
	for rel := range map[string]bool{"internal/a/bad.go": true} {
		write(rel, text)
	}
	var hits []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skippedDirNames[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if scanned(rel) && len(findings(mustRead(t, path))) > 0 {
			hits = append(hits, rel)
		}
		return nil
	})
	if want := []string{"internal/a/bad.go"}; !reflect.DeepEqual(hits, want) {
		t.Errorf("hits = %v, want %v", hits, want)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
