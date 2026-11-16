package exercise_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	exercise "github.com/cybericebox/backend/internal/useCase/exercise"
)

const archiveSchemaFile = brainFixtures + "/schema/cib-exercise-1.0.schema.json"

// The committed schema is exactly what the generator writes from the structs of archive_v2.go; a changed struct fails here until
// `go generate ./internal/useCase/exercise` (or -update-brain-fixtures) rewrites the file.
func TestArchiveSchemaIsUpToDate(t *testing.T) {
	got, err := exercise.ArchiveDocumentSchema()
	require.NoError(t, err)
	if *updateBrainFixtures {
		require.NoError(t, os.MkdirAll(filepath.Dir(archiveSchemaFile), 0o755))
		require.NoError(t, os.WriteFile(archiveSchemaFile, got, 0o644))
		return
	}
	want, err := os.ReadFile(archiveSchemaFile)
	require.NoError(t, err, "missing schema: run go generate ./internal/useCase/exercise")
	require.Equal(t, string(want), string(got), "schema is out of date: run go generate ./internal/useCase/exercise")
}

func compileArchiveSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile(archiveSchemaFile)
	require.NoError(t, err)
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	require.NoError(t, err)
	compiler := jsonschema.NewCompiler()
	require.NoError(t, compiler.AddResource("cib-exercise.schema.json", doc))
	schema, err := compiler.Compile("cib-exercise.schema.json")
	require.NoError(t, err)
	return schema
}

func validateDocument(t *testing.T, schema *jsonschema.Schema, document []byte) error {
	t.Helper()
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(document))
	require.NoError(t, err)
	return schema.Validate(value)
}

func TestArchiveSchemaAcceptsTheSamples(t *testing.T) {
	schema := compileArchiveSchema(t)

	t.Run("edit export", func(t *testing.T) {
		document, err := os.ReadFile(brainFixtures + "/edit/exercise.json")
		require.NoError(t, err)
		require.NoError(t, validateDocument(t, schema, document))
	})
	t.Run("create sample", func(t *testing.T) {
		document, err := os.ReadFile(brainFixtures + "/create/exercise.json")
		require.NoError(t, err)
		require.NoError(t, validateDocument(t, schema, document))
	})
	t.Run("web-dynamic yaml", func(t *testing.T) {
		source, err := os.ReadFile(brainFixtures + "/tasks/web-dynamic/exercise.yaml")
		require.NoError(t, err)
		var tree any
		require.NoError(t, yaml.Unmarshal(source, &tree))
		document, err := json.Marshal(tree)
		require.NoError(t, err)
		require.NoError(t, validateDocument(t, schema, document))
	})
}

func TestArchiveSchemaRefusesWhatTheDecoderRefuses(t *testing.T) {
	schema := compileArchiveSchema(t)
	for name, document := range map[string]string{
		"unknown key":      `{"schema":"cib-exercise/1.0","exercise":{"name":"x"},"versions":[{"variants":[]}],"extra":1}`,
		"wrong case":       `{"schema":"cib-exercise/1.0","Exercise":{"name":"x"},"versions":[{"variants":[]}]}`,
		"wrong schema":     `{"schema":"cib-exercise/2.0","exercise":{"name":"x"},"versions":[{"variants":[]}]}`,
		"no name":          `{"schema":"cib-exercise/1.0","exercise":{},"versions":[{"variants":[]}]}`,
		"bad difficulty":   `{"schema":"cib-exercise/1.0","exercise":{"name":"x"},"versions":[{"variants":[{"tasks":[{"name":"t","difficulty":"brutal"}]}]}]}`,
		"device no type":   `{"schema":"cib-exercise/1.0","exercise":{"name":"x"},"versions":[{"variants":[{"topology":{"devices":[{"name":"d"}]}}]}]}`,
		"missing versions": `{"schema":"cib-exercise/1.0","exercise":{"name":"x"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, validateDocument(t, schema, []byte(document)))
		})
	}
}
