package brainapiv1_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	brainapiv1 "github.com/cybericebox/backend/pkg/brainapi/v1"
)

// TestServiceMethods fails when the Brain service of the contract loses or gains a method.
func TestServiceMethods(t *testing.T) {
	want := []string{
		"Enroll", "Renew", "UploadMaterials", "StartRun", "RunEvents", "PostVerdict",
		"PostLabFacts", "StopRun", "GetOpponent", "PushData", "UploadTaskArchive", "Requests",
	}
	svc := brainapiv1.File_brain_proto.Services().ByName("Brain")
	if svc == nil {
		t.Fatal("service Brain is missing from brain.proto")
	}
	if got := string(svc.FullName()); got != "cybericebox.brain.v1.Brain" {
		t.Fatalf("service full name = %q", got)
	}
	var got []string
	methods := svc.Methods()
	for i := range methods.Len() {
		got = append(got, string(methods.Get(i).Name()))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("methods = %v, want %v", got, want)
	}

	desc := brainapiv1.Brain_ServiceDesc
	if desc.ServiceName != "cybericebox.brain.v1.Brain" {
		t.Fatalf("gRPC service name = %q", desc.ServiceName)
	}
	if n := len(desc.Methods) + len(desc.Streams); n != len(want) {
		t.Fatalf("gRPC service descriptor has %d methods, want %d", n, len(want))
	}
}

// TestNoBackendImports keeps the package importable on its own: a consumer (the Brain) must not
// compile any other package of this module into its binary.
func TestNoBackendImports(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parser.ParseFile(fset, f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(path, "github.com/cybericebox/") {
				t.Errorf("%s imports %s: the contract package must import only grpc and protobuf", f, path)
			}
		}
	}
}
