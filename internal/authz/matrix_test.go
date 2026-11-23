package authz

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateMatrix = flag.Bool("update", false, "rewrite testdata/access-matrix.golden")

const matrixPath = "testdata/access-matrix.golden"

// TestAccessMatrix pins built-in role × key × scope type (spec §10). A change to a role or a key flag shows up as a
// diff of testdata/access-matrix.golden that a reviewer reads as the rule change; rerun with -update after a
// deliberate change.
func TestAccessMatrix(t *testing.T) {
	got := AccessMatrix()
	if *updateMatrix {
		if err := os.MkdirAll(filepath.Dir(matrixPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(matrixPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(matrixPath)
	if err != nil {
		t.Fatalf("read %s: %v (run with -update to create it)", matrixPath, err)
	}
	if got == string(want) {
		return
	}
	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(string(want), "\n")
	wantSet, gotSet := map[string]bool{}, map[string]bool{}
	for _, l := range wantLines {
		wantSet[l] = true
	}
	for _, l := range gotLines {
		gotSet[l] = true
	}
	var diff []string
	for _, l := range wantLines {
		if !gotSet[l] {
			diff = append(diff, "- "+l)
		}
	}
	for _, l := range gotLines {
		if !wantSet[l] {
			diff = append(diff, "+ "+l)
		}
	}
	t.Fatalf("the access matrix differs from %s (review the rule change, then run with -update):\n%s", matrixPath, strings.Join(diff, "\n"))
}

func TestAccessMatrixListsEveryRoleAndKey(t *testing.T) {
	m := AccessMatrix()
	for _, role := range BuiltinRoles() {
		if !strings.Contains(m, "\n"+string(role.Name)+" ") {
			t.Errorf("no rows for %s", role.Name)
		}
	}
	// super_admin covers every key, so every key appears in its rows.
	for _, spec := range Catalogue() {
		if !strings.Contains(m, " "+string(spec.Key)+" ") {
			t.Errorf("no row for %s", spec.Key)
		}
	}
}
