package exercise_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	postgresMocks "github.com/cybericebox/backend/internal/delivery/repository/postgres/mocks"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	"github.com/cybericebox/backend/internal/model/rbac"
	"github.com/cybericebox/backend/internal/useCase/exercise"
	"github.com/cybericebox/backend/pkg/secret"
)

// The dossier is the one kept at the deploy (a later draft edit changes nothing), with its secret values opened at read time; it
// is refused for an event preview and to a caller without author access, checked on every read.
func TestTestDeployDossier(t *testing.T) {
	cipher, err := secret.New(strings.Repeat("88", 32))
	require.NoError(t, err)
	variantID, versionID, exerciseID, owner, deployID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	secretCred := exerciseModel.DefenderCredential{Device: "r1", Port: "console0", User: "admin", Secret: true}
	sealed, err := cipher.EncryptWithContext([]byte("s3cret"), exercise.DefenderSecretContext(variantID, secretCred))
	require.NoError(t, err)
	secretCred.Value = sealed
	kept := exerciseModel.DeployedDossier{Variant: variantID, Dossier: exerciseModel.Dossier{
		Links:     []exerciseModel.ConsoleLink{{SerialPort: "serial0", Path: "/dev/ttyCON0", Device: "r1", ConsolePort: "console0", Kind: exerciseModel.ConsoleKindLogin}},
		Forbidden: []string{"db"}, Operates: []string{"r1"}, Topology: exerciseModel.DefenderTopologyFull,
		Credentials: []exerciseModel.DefenderCredential{secretCred, {Device: "web", Value: "plain"}},
	}}
	stored, err := json.Marshal(kept)
	require.NoError(t, err)
	// The draft has changed since: another seat, no credentials. The dossier is the deployed one.
	variants, err := json.Marshal([]exerciseModel.Variant{{ID: variantID, Defender: &exerciseModel.Defender{Seat: "other"}}})
	require.NoError(t, err)

	setup := func(t *testing.T, row postgres.ExerciseTestDeployment) (*exercise.ExerciseUseCase, *postgresMocks.MockQuerier) {
		q := postgresMocks.NewMockQuerier(gomock.NewController(t))
		q.EXPECT().GetOwnedExerciseTestDeploy(gomock.Any(), gomock.Any()).Return(row, nil).AnyTimes()
		q.EXPECT().GetExerciseVersionByID(gomock.Any(), versionID).Return(postgres.ExerciseVersion{ID: versionID, ExerciseID: exerciseID, Variants: variants}, nil).AnyTimes()
		infra := &fakeInfra{status: exerciseModel.LabDeployStatus{Phase: "Ready", Ready: true}}
		return exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Infra: infra, VPNStore: &fakeVPNStore{}, Cipher: cipher}), q
	}
	row := postgres.ExerciseTestDeployment{ID: deployID, GroupName: "t-group", LabName: "t-lab", CreatedBy: owner, VersionID: versionID, VariantID: variantID, Dossier: stored}
	admin := exercise.Actor{UserID: owner, Role: rbac.RoleAdmin}

	uc, _ := setup(t, row)
	view, err := uc.TestDeployDossier(context.Background(), admin, deployID)
	require.NoError(t, err)
	require.True(t, view.Declared)
	require.Equal(t, kept.Links, view.Links)
	require.Equal(t, []string{"db"}, view.Forbidden)
	require.Equal(t, []exerciseModel.DefenderCredential{{Device: "r1", Port: "console0", User: "admin", Value: "s3cret"}, {Device: "web", Value: "plain"}}, view.Credentials)

	preview := row
	preview.EventID = uuid.NullUUID{UUID: uuid.Must(uuid.NewV7()), Valid: true}
	uc, _ = setup(t, preview)
	_, err = uc.TestDeployDossier(context.Background(), admin, deployID)
	require.True(t, errors.Is(err, exerciseModel.ErrTestDeployNotFound.Err()), "never for an event preview: %v", err)

	uc, q := setup(t, row)
	q.EXPECT().GetExerciseByID(gomock.Any(), exerciseID).Return(postgres.Exercise{ID: exerciseID}, nil).AnyTimes()
	_, err = uc.TestDeployDossier(context.Background(), exercise.Actor{UserID: owner, Role: rbac.RoleUser}, deployID)
	require.True(t, errors.Is(err, exerciseModel.ErrExerciseForbidden.Err()), "a caller without author access: %v", err)

	none := row
	none.Dossier = nil
	uc, _ = setup(t, none)
	view, err = uc.TestDeployDossier(context.Background(), admin, deployID)
	require.NoError(t, err)
	require.False(t, view.Declared)
	require.Empty(t, view.Credentials)
}
