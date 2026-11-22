package exercise_test

import (
	"context"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	postgresMocks "github.com/cybericebox/backend/internal/delivery/repository/postgres/mocks"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	exercise "github.com/cybericebox/backend/internal/useCase/exercise"
	"github.com/cybericebox/backend/pkg/secret"
)

// A task made automatically declares its secrets with empty values. Saving it keeps them empty (no error), the version says
// which values are missing, and the editor entering a value clears the entry (the stored empty one is never "kept" over it).
func TestSaveDraftWithAnEmptySecretMarksItMissingAndEnteringTheValueClearsIt(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := postgresMocks.NewMockQuerier(ctrl)
	cipher, _ := secret.New(testKey)
	uc := newTestExerciseUseCase(exercise.Dependencies{Repo: q, Cipher: cipher, Media: newFakeMedia()})
	exID := uuid.Must(uuid.NewV7())
	expectEditableExercise(q, exID)

	// the stored draft: the secret declared, no value
	stored := mustMarshal(t, draftVariants("", true))
	q.EXPECT().GetDraftVersion(gomock.Any(), exID).Return(postgres.ExerciseVersion{
		ID: uuid.Must(uuid.NewV7()), ExerciseID: exID, Status: "draft", Variants: stored,
	}, nil).AnyTimes()
	var captured postgres.UpsertExerciseDraftParams
	expectUpsertCapture(t, q, &captured)

	view, err := uc.SaveDraft(context.Background(), exID, exercise.SaveDraftInput{Variants: draftVariants("", true)})
	require.NoError(t, err)
	assert.True(t, view.HasMissingSecrets)
	assert.Equal(t, []exerciseModel.MissingSecret{{Variant: 1, Device: "web", Key: "DB_PASS"}}, view.MissingSecrets)
	assert.Equal(t, "", view.Variants[0].Topology.Devices[0].Bootstrap.Env[0].Value, "a view never carries a secret value")

	expectUpsertCapture(t, q, &captured)
	view, err = uc.SaveDraft(context.Background(), exID, exercise.SaveDraftInput{Variants: draftVariants("the value", true)})
	require.NoError(t, err)
	assert.False(t, view.HasMissingSecrets)
	assert.Empty(t, view.MissingSecrets)
	assert.NotContains(t, string(captured.Variants), "the value", "the value is stored sealed")
}

// An archive that declares a secret without a value imports: it is not a secret that needs a password, and nothing refuses it.
func TestAnArchiveWithAnEmptySecretNeedsNoPasswordAndParses(t *testing.T) {
	data := exportFixture(t)
	parsed, err := exercise.ParseExerciseArchive(data, "", exercise.DefaultLimits())
	require.NoError(t, err)
	for vi := range parsed.Versions {
		for ai := range parsed.Versions[vi].Variants {
			for di := range parsed.Versions[vi].Variants[ai].Topology.Devices {
				device := &parsed.Versions[vi].Variants[ai].Topology.Devices[di]
				device.Bootstrap.Env = append(device.Bootstrap.Env, exerciseModel.EnvVar{Name: "DB_PASSWORD", Secret: true})
			}
		}
	}
	assert.NotEmpty(t, exerciseModel.MissingSecrets(parsed.Versions[0].Variants), "the declared secrets are missing")
	assert.Empty(t, exercise.FirstSecretValueForTest(parsed.Versions), "an empty secret is no value: no password is asked for")
}

// The version read tells which secrets are missing and says nothing about the ones that have a value.
func TestGetVersionListsTheMissingSecrets(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := postgresMocks.NewMockQuerier(ctrl)
	uc := newUC(q, newFakeMedia())
	exID, vID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for name, c := range map[string]struct {
		stored  string
		missing bool
	}{"declared without a value": {"", true}, "sealed value": {"ciphertext-blob", false}} {
		t.Run(name, func(t *testing.T) {
			q.EXPECT().GetExerciseVersionByID(gomock.Any(), vID).Return(postgres.ExerciseVersion{
				ID: vID, ExerciseID: exID, Status: "draft", Variants: mustMarshal(t, draftVariants(c.stored, true)),
			}, nil)
			view, err := uc.GetVersion(context.Background(), exID, vID)
			require.NoError(t, err)
			assert.Equal(t, c.missing, view.HasMissingSecrets)
			assert.Equal(t, c.missing, len(view.MissingSecrets) == 1)
		})
	}
}
