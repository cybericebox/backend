// Command archiveschema writes the JSON Schema of the exercise document of a task archive (cib-exercise/1.0), generated from
// the Go structs the importer decodes. Usage:
// go run ./tools/archiveschema -o testdata/brain/schema/cib-exercise-1.0.schema.json
// (or `go generate ./internal/useCase/exercise`). Without -o it writes to the standard output.
package main

import (
	"flag"
	"fmt"
	"os"

	exercise "github.com/cybericebox/backend/internal/useCase/exercise"
)

func main() {
	out := flag.String("o", "", "the file to write (default: the standard output)")
	flag.Parse()
	schema, err := exercise.ArchiveDocumentSchema()
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
