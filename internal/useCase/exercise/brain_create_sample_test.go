package exercise_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	postgresMocks "github.com/cybericebox/backend/internal/delivery/repository/postgres/mocks"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	"github.com/cybericebox/backend/internal/model/rbac"
	exercise "github.com/cybericebox/backend/internal/useCase/exercise"
)

// testdata/brain/create is the archive of a brand-new task as the brain should return it: the edit sample of the same task with
// every platform id taken out (a new task has none) and the secret variable declared with an empty value. It is made here from
// the daemon's own code; the README states the format. Regenerate with:
//
//	go test ./internal/useCase/exercise -run TestBrainCreateSample -update-brain-fixtures
const brainCreateDir = brainFixtures + "/create"

// withoutIDs removes the ids the platform gives (exercise, version, variant, task, hint, device).
func withoutIDs(t *testing.T, doc []byte) []byte {
	t.Helper()
	var root map[string]any
	require.NoError(t, json.Unmarshal(doc, &root))
	drop := func(v any) map[string]any {
		object, _ := v.(map[string]any)
		delete(object, "id")
		return object
	}
	drop(root["exercise"])
	delete(root, "edit") // what an edit started from: a new task has none
	for _, version := range root["versions"].([]any) {
		for _, variant := range drop(version)["variants"].([]any) {
			object := drop(variant)
			for _, task := range object["tasks"].([]any) {
				taskObject := drop(task)
				for _, hint := range asList(taskObject["hints"]) {
					drop(hint)
				}
			}
			for _, device := range object["topology"].(map[string]any)["devices"].([]any) {
				drop(device)
			}
		}
	}
	out, err := json.Marshal(root)
	require.NoError(t, err)
	return out
}

func asList(v any) []any {
	list, _ := v.([]any)
	return list
}

func createSampleArchive(t *testing.T) []byte {
	t.Helper()
	files, err := exercise.ReadArchiveV1(bytes.NewReader(editSampleArchive(t)), exercise.DefaultLimits())
	require.NoError(t, err)
	files["exercise.json"] = withoutIDs(t, files["exercise.json"])
	archive, err := exercise.BuildArchiveV1(files)
	require.NoError(t, err)
	return archive
}

func TestBrainCreateSample(t *testing.T) {
	archive := createSampleArchive(t)
	files, err := exercise.ReadArchiveV1(bytes.NewReader(archive), exercise.DefaultLimits())
	require.NoError(t, err)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	listing := strings.Join(names, "\n") + "\n"

	if *updateBrainFixtures {
		require.NoError(t, os.MkdirAll(brainCreateDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(brainCreateDir, "sample.cybericebox.zip"), archive, 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(brainCreateDir, "entries.txt"), []byte(listing), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(brainCreateDir, "exercise.json"), indented(t, files["exercise.json"]), 0o644))
		return
	}
	committed, err := os.ReadFile(filepath.Join(brainCreateDir, "sample.cybericebox.zip"))
	require.NoError(t, err, "missing fixture: run go test ./internal/useCase/exercise -run TestBrainCreateSample -update-brain-fixtures")
	have, err := exercise.ReadArchiveV1(bytes.NewReader(committed), exercise.DefaultLimits())
	require.NoError(t, err)
	require.Equal(t, files, have, "testdata/brain/create/sample.cybericebox.zip is out of date: run go test ./internal/useCase/exercise -run TestBrainCreateSample -update-brain-fixtures")
	want, err := os.ReadFile(filepath.Join(brainCreateDir, "entries.txt"))
	require.NoError(t, err)
	require.Equal(t, string(want), listing)
	doc, err := os.ReadFile(filepath.Join(brainCreateDir, "exercise.json"))
	require.NoError(t, err)
	require.Equal(t, string(doc), string(indented(t, files["exercise.json"])))
}

// The sample is what the importer reads for a create: it parses, names no task (so it is a new one, whoever saves it), holds no
// secret value, and is publishable but for the secret value the author still has to enter.
func TestBrainCreateSampleIsAnImportableNewTask(t *testing.T) {
	archive := createSampleArchive(t)
	parsed, err := exercise.ParseExerciseArchive(archive, "", exercise.DefaultLimits())
	require.NoError(t, err)
	assert.True(t, parsed.SourceExerciseID.IsNil(), "a create names no task")
	require.Len(t, parsed.Versions, 1)
	assert.Empty(t, exercise.FirstSecretValueForTest(parsed.Versions), "the brain sets no secret value")
	version := parsed.Versions[0]
	assert.Equal(t, []exerciseModel.MissingSecret{{Variant: 1, Device: "web", Key: "DB_PASSWORD"}, {Variant: 2, Device: "web", Key: "DB_PASSWORD"}}, version.MissingSecrets())

	q := postgresMocks.NewMockQuerier(gomock.NewController(t))
	uc := newTestExerciseUseCase(exercise.Dependencies{Repo: q, Media: exportFixtureMedia()})
	target, err := uc.EditedArchiveTarget(context.Background(), exercise.Actor{Role: rbac.RoleAdmin}, archive, exercise.DefaultLimits())
	require.NoError(t, err)
	assert.True(t, target.ExerciseID.IsNil(), "it is saved as a new task")
}
