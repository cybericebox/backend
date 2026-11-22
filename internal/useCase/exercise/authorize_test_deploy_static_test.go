package exercise_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gofrs/uuid"
	"go.uber.org/mock/gomock"

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	postgresMocks "github.com/cybericebox/backend/internal/delivery/repository/postgres/mocks"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	"github.com/cybericebox/backend/internal/model/rbac"
	"github.com/cybericebox/backend/internal/useCase/exercise"
)

// An event whose infrastructure is forbidden can still test a static variant (no devices); a lab variant of the
// same exercise stays refused.
func TestAuthorizeTestDeploy_EventWithoutInfrastructure(t *testing.T) {
	userID, eventID, exerciseID, versionID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	staticID, labID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	variants, _ := json.Marshal([]exerciseModel.Variant{
		{ID: staticID},
		{ID: labID, Topology: exerciseModel.Topology{Devices: []exerciseModel.Device{{Name: "web"}}}},
	})
	for _, tc := range []struct {
		name    string
		variant uuid.UUID
		wantErr error
	}{
		{name: "static variant starts", variant: staticID},
		{name: "lab variant is refused", variant: labID, wantErr: exerciseModel.ErrExerciseInfrastructureNotAllowed.Err()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := postgresMocks.NewMockQuerier(gomock.NewController(t))
			q.EXPECT().GetExerciseVersionByID(gomock.Any(), versionID).Return(postgres.ExerciseVersion{ID: versionID, ExerciseID: exerciseID, Variants: variants}, nil).AnyTimes()
			q.EXPECT().GetExerciseByID(gomock.Any(), exerciseID).Return(postgres.Exercise{ID: exerciseID, Scope: 1, OwnerEventID: uuid.NullUUID{UUID: eventID, Valid: true}}, nil).AnyTimes()
			q.EXPECT().GetEventInfrastructureAllowed(gomock.Any(), eventID).Return(false, nil).AnyTimes()
			uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Media: newFakeMedia()})
			err := uc.AuthorizeTestDeploy(context.Background(), exercise.Actor{UserID: userID, Role: rbac.RoleSuperAdmin}, exerciseID, versionID, tc.variant)
			if !errors.Is(err, tc.wantErr) && (tc.wantErr != nil || err != nil) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
		})
	}
}

// A test deploy is authorized for the exercise the ids are bound to: the version must be that exercise's and the variant
// that version's; any other mix is not found, whoever may write the exercise named in the path.
func TestAuthorizeTestDeploy_IdsMustBeBoundTogether(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	exerciseA, exerciseB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	versionA, versionB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	variantA, variantB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	variantsA, _ := json.Marshal([]exerciseModel.Variant{{ID: variantA, Topology: exerciseModel.Topology{Devices: []exerciseModel.Device{{Name: "web"}}}}})
	variantsB, _ := json.Marshal([]exerciseModel.Variant{{ID: variantB, Topology: exerciseModel.Topology{Devices: []exerciseModel.Device{{Name: "web"}}}}})
	q := postgresMocks.NewMockQuerier(gomock.NewController(t))
	q.EXPECT().GetExerciseVersionByID(gomock.Any(), versionA).Return(postgres.ExerciseVersion{ID: versionA, ExerciseID: exerciseA, Variants: variantsA}, nil).AnyTimes()
	q.EXPECT().GetExerciseVersionByID(gomock.Any(), versionB).Return(postgres.ExerciseVersion{ID: versionB, ExerciseID: exerciseB, Variants: variantsB}, nil).AnyTimes()
	q.EXPECT().GetExerciseByID(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, id uuid.UUID) (postgres.Exercise, error) {
		return postgres.Exercise{ID: id}, nil
	}).AnyTimes()
	uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Media: newFakeMedia()})
	admin := exercise.Actor{UserID: userID, Role: rbac.RoleSuperAdmin}
	notFound := exerciseModel.ErrExerciseVersionNotFound.Err()

	if err := uc.AuthorizeTestDeploy(context.Background(), admin, exerciseA, versionA, variantA); err != nil {
		t.Fatalf("the bound ids are authorized: %v", err)
	}
	for name, ids := range map[string][3]uuid.UUID{
		"a version of another exercise":           {exerciseA, versionB, variantB},
		"a variant of another version":            {exerciseA, versionA, variantB},
		"a variant of another exercise's version": {exerciseB, versionB, variantA},
		"an unknown variant":                      {exerciseA, versionA, uuid.Must(uuid.NewV7())},
	} {
		if err := uc.AuthorizeTestDeploy(context.Background(), admin, ids[0], ids[1], ids[2]); !errors.Is(err, notFound) {
			t.Errorf("%s: want not found, got %v", name, err)
		}
	}
	// the deploy itself never reads a variant of another version either
	if _, err := uc.DeployVariantTest(context.Background(), userID, versionA, variantB, 0); !errors.Is(err, notFound) {
		t.Errorf("deploy with a variant of another version: %v", err)
	}
}
