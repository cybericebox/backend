// Command archiveschema writes the JSON Schema of the exercise document of a task archive (one version, cib-exercise/1.0 or
// cib-exercise/1.1), generated from the Go structs the importer decodes. Usage:
// go run ./tools/archiveschema -schema cib-exercise/1.1 -o testdata/brain/schema/cib-exercise-1.1.schema.json
// (or `go generate ./internal/useCase/exercise`, which writes every version). Without -o it writes to the standard output.
package main

import (
	"flag"
	"fmt"
	"os"

	exercise "github.com/cybericebox/backend/internal/useCase/exercise"
)

func main() {
	out := flag.String("o", "", "the file to write (default: the standard output)")
	version := flag.String("schema", "cib-exercise/1.0", "the document version")
	flag.Parse()
	schema, err := exercise.ArchiveDocumentSchema(*version)
	if err != nil {
		fmt.Fprintln(os.Stderr, "archiveschema:", err)
		os.Exit(1)
	}
	if *out == "" {
		_, _ = os.Stdout.Write(schema)
		return
	}
	if err = os.WriteFile(*out, schema, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "archiveschema:", err)
		os.Exit(1)
	}
}
