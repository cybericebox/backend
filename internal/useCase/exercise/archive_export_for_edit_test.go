package exercise_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	postgresMocks "github.com/cybericebox/backend/internal/delivery/repository/postgres/mocks"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	"github.com/cybericebox/backend/internal/model/rbac"
	exercise "github.com/cybericebox/backend/internal/useCase/exercise"
)

func exportForEdit(t *testing.T, actor exercise.Actor, id, version uuid.UUID, arrange func(q *postgresMocks.MockQuerier)) ([]byte, error) {
	t.Helper()
	ctrl := gomock.NewController(t)
	q := postgresMocks.NewMockQuerier(ctrl)
	// no working copy: the version asked for is exported as it is
	q.EXPECT().GetDraftRevision(gomock.Any(), id).Return(postgres.GetDraftRevisionRow{}, pgx.ErrNoRows).AnyTimes()
	arrange(q)
	uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Media: exportFixtureMedia()})
	return uc.ExportTaskVersionForEdit(context.Background(), actor, id, version)
}

// An editor gets the archive of the one version asked for, in the format of the admin export, and nothing of the other versions.
func TestExportTaskVersionForEdit_OneVersionOfTheTask(t *testing.T) {
	admin := exercise.Actor{UserID: uuid.Must(uuid.NewV7()), Role: rbac.RoleAdmin}
	data, err := exportForEdit(t, admin, exportExerciseID, exportID(901), func(q *postgresMocks.MockQuerier) {
		expectExport(t, q, exportExerciseID, "Експорт", exportFixtureVersions())
	})
	require.NoError(t, err)
	doc := exportedDocument(t, data)
	var top struct {
		Schema   string `json:"schema"`
		Versions []struct {
			Label string `json:"label"`
		} `json:"versions"`
	}
	require.NoError(t, json.Unmarshal(doc, &top))
	require.Equal(t, "cib-exercise/1.0", top.Schema)
	require.Len(t, top.Versions, 1)
	require.Equal(t, "Snapshot", top.Versions[0].Label)
	require.False(t, bytes.Contains(doc, []byte("note")), "the other version is not in the archive")
	_, err = exercise.ParseExerciseArchive(data, "", exercise.DefaultLimits())
	require.NoError(t, err, "it is an import archive")
}

// A version that is not of the task is not found, not a silent empty archive.
func TestExportTaskVersionForEdit_WrongVersion(t *testing.T) {
	admin := exercise.Actor{UserID: uuid.Must(uuid.NewV7()), Role: rbac.RoleAdmin}
	_, err := exportForEdit(t, admin, exportExerciseID, exportID(7777), func(q *postgresMocks.MockQuerier) {
		expectExport(t, q, exportExerciseID, "Експорт", exportFixtureVersions())
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, exerciseModel.ErrExerciseVersionNotFound.Err()), "got %v", err)
}

// A user who may not edit the task gets forbidden before anything of the task is read.
func TestExportTaskVersionForEdit_Forbidden(t *testing.T) {
	user := exercise.Actor{UserID: uuid.Must(uuid.NewV7()), Role: rbac.RoleUser}
	_, err := exportForEdit(t, user, exportExerciseID, exportID(901), func(q *postgresMocks.MockQuerier) {
		q.EXPECT().GetExerciseByID(gomock.Any(), exportExerciseID).Return(postgres.Exercise{ID: exportExerciseID}, nil)
	})
	require.True(t, errors.Is(err, exerciseModel.ErrExerciseForbidden.Err()), "got %v", err)
}
