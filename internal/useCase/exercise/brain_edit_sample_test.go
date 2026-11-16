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

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	postgresMocks "github.com/cybericebox/backend/internal/delivery/repository/postgres/mocks"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	"github.com/cybericebox/backend/internal/model/rbac"
	exercise "github.com/cybericebox/backend/internal/useCase/exercise"
	"github.com/cybericebox/backend/pkg/secret"
)

// testdata/brain/edit holds the archive the platform gives the brain to edit an existing task (ExportTaskVersionForEdit): the
// task of the export fixtures with two variants, a device with an ordinary variable and a secret one. The files are made here
// from the daemon's own code; the README states what the brain must keep. Regenerate with:
//
//	go test ./internal/useCase/exercise -run TestBrainEditSample -update-brain-fixtures
const brainEditDir = brainFixtures + "/edit"

func editSampleArchive(t *testing.T) []byte {
	t.Helper()
	cipher, err := secret.New(testKey)
	require.NoError(t, err)
	versions := exportFixtureVersions()[:1] // the draft with two variants
	for vi := range versions {
		for ai := range versions[vi].Variants {
			variant := &versions[vi].Variants[ai]
			for di := range variant.Topology.Devices {
				device := &variant.Topology.Devices[di]
				if device.Name != "web" {
					continue
				}
				sealed, sealErr := cipher.EncryptWithContext([]byte("the stored password"), exercise.EnvSecretContext(variant.ID, device.Name, "DB_PASSWORD"))
				require.NoError(t, sealErr)
				device.Bootstrap.Env = append(device.Bootstrap.Env, exerciseModel.EnvVar{Name: "DB_PASSWORD", Secret: true, Value: sealed, Origin: "stored-origin"})
			}
		}
	}
	q := postgresMocks.NewMockQuerier(gomock.NewController(t))
	expectExport(t, q, exportExerciseID, "Експорт", versions)
	uc := newTestExerciseUseCase(exercise.Dependencies{Repo: q, Cipher: cipher, Media: exportFixtureMedia()})
	archive, err := uc.ExportTaskVersionForEdit(context.Background(), exercise.Actor{UserID: exportID(1), Role: rbac.RoleAdmin}, exportExerciseID, versions[0].ID)
	require.NoError(t, err)
	return archive
}

func TestBrainEditSample(t *testing.T) {
	archive := editSampleArchive(t)
	files, err := exercise.ReadArchiveV1(bytes.NewReader(archive), exercise.DefaultLimits())
	require.NoError(t, err)
	require.NotContains(t, string(files["exercise.json"]), "the stored password", "the archive carries no secret value")

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	listing := strings.Join(names, "\n") + "\n"

	if *updateBrainFixtures {
		require.NoError(t, os.MkdirAll(brainEditDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(brainEditDir, "sample.cybericebox.zip"), archive, 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(brainEditDir, "entries.txt"), []byte(listing), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(brainEditDir, "exercise.json"), indented(t, files["exercise.json"]), 0o644))
		return
	}
	// The committed archive is the one the code makes now: same entries, same bytes in each.
	committed, err := os.ReadFile(filepath.Join(brainEditDir, "sample.cybericebox.zip"))
	require.NoError(t, err, "missing fixture: run go test ./internal/useCase/exercise -run TestBrainEditSample -update-brain-fixtures")
	have, err := exercise.ReadArchiveV1(bytes.NewReader(committed), exercise.DefaultLimits())
	require.NoError(t, err)
	require.Equal(t, files, have, "testdata/brain/edit/sample.cybericebox.zip is out of date: run go test ./internal/useCase/exercise -run TestBrainEditSample -update-brain-fixtures")
	want, err := os.ReadFile(filepath.Join(brainEditDir, "entries.txt"))
	require.NoError(t, err)
	require.Equal(t, string(want), listing)
	doc, err := os.ReadFile(filepath.Join(brainEditDir, "exercise.json"))
	require.NoError(t, err)
	require.Equal(t, string(doc), string(indented(t, files["exercise.json"])))
}

func indented(t *testing.T, doc []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, jsonIndent(&out, doc))
	out.WriteByte('\n')
	return out.Bytes()
}

func jsonIndent(out *bytes.Buffer, doc []byte) error { return json.Indent(out, doc, "", "  ") }
