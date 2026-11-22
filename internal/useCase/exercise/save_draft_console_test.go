package exercise_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gofrs/uuid"
	"go.uber.org/mock/gomock"

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	postgresMocks "github.com/cybericebox/backend/internal/delivery/repository/postgres/mocks"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	exercise "github.com/cybericebox/backend/internal/useCase/exercise"
	"github.com/cybericebox/backend/pkg/secret"
)

// A working-copy save, which skips the business validation, still refuses a network connection that ends on a port (a
// client that dropped the ports and the kind of a console cable) and an unknown connection kind, before anything is written.
func TestSaveDraft_RefusesAConsoleCableTurnedIntoANetworkConnection(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*exerciseModel.Variant)
		want   error
	}{
		"ports dropped, kind dropped": {func(v *exerciseModel.Variant) {
			d := v.Topology.Devices[0]
			other := exerciseModel.Device{ID: uuid.Must(uuid.NewV7()), Name: "r1", Type: exerciseModel.DeviceTypeNode}
			v.Topology.Devices = append(v.Topology.Devices, other)
			v.Topology.Connections = append(v.Topology.Connections, exerciseModel.Connection{Endpoints: []exerciseModel.Endpoint{
				{Kind: exerciseModel.EndpointDevice, DeviceID: d.ID, Interface: "serial0"},
				{Kind: exerciseModel.EndpointDevice, DeviceID: other.ID, Interface: "console0"},
			}})
		}, exerciseModel.ErrConsolePortOnNetwork.Err()},
		"unknown kind": {func(v *exerciseModel.Variant) {
			v.Topology.Connections = append(v.Topology.Connections, exerciseModel.Connection{Kind: "foo"})
		}, exerciseModel.ErrConnectionKindInvalid.Err()},
	} {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			q := postgresMocks.NewMockQuerier(ctrl)
			cipher, _ := secret.New(testKey)
			uc := newTestExerciseUseCase(exercise.Dependencies{Repo: q, Cipher: cipher, Media: newFakeMedia()})
			exID := uuid.Must(uuid.NewV7())
			q.EXPECT().GetDraftVersion(gomock.Any(), exID).Return(postgres.ExerciseVersion{}, errNoRows()).AnyTimes()
			q.EXPECT().GetExerciseByID(gomock.Any(), exID).Return(postgres.Exercise{ID: exID}, nil).AnyTimes()

			variants := draftVariants("s3cret", true)
			tc.mutate(&variants[0])
			_, err := uc.SaveDraft(context.Background(), exID, exercise.SaveDraftInput{Variants: variants})
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}
