package exercise

import (
	"context"
	"errors"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	imageRegistryUseCase "github.com/cybericebox/backend/internal/useCase/imageRegistry"
)

// toolsImages is an image access that knows the start configuration of one tools image, and records the credential it is read with.
type toolsImages struct {
	known map[string]exerciseModel.ImageService
	pulls []*exerciseModel.ToolsPull
}

func (f *toolsImages) ExerciseImageScope(context.Context, uuid.UUID) (imageRegistryUseCase.Scope, error) {
	return imageRegistryUseCase.Scope{}, nil
}

func (f *toolsImages) PrepareDeployImages(_ context.Context, _ imageRegistryUseCase.Scope, topo exerciseModel.Topology) (imageRegistryUseCase.DeployImages, error) {
	return imageRegistryUseCase.DeployImages{Topology: topo}, nil
}

func (f *toolsImages) ToolsImageService(_ context.Context, image string, pull *exerciseModel.ToolsPull) (exerciseModel.ImageService, bool) {
	f.pulls = append(f.pulls, pull)
	service, ok := f.known[image]
	return service, ok
}

// A tools device that starts through the launcher (serial ports from auto-cabling) gets its image's start configuration, read
// with the brain's credential; one that does not is left alone; an unknown configuration refuses the deploy.
func TestWithToolsServices(t *testing.T) {
	cabled := exerciseModel.Device{ID: uuid.Must(uuid.NewV7()), Name: "tools", Type: exerciseModel.DeviceTypeNode, Image: "brain/defender:1",
		SerialPorts: []exerciseModel.SerialPort{{Name: "serial0"}}}
	plain := exerciseModel.Device{ID: uuid.Must(uuid.NewV7()), Name: "tools-2", Type: exerciseModel.DeviceTypeNode, Image: "brain/attacker:1"}
	author := exerciseModel.Device{ID: uuid.Must(uuid.NewV7()), Name: "pc1", Type: exerciseModel.DeviceTypeNode, Image: "pc:1",
		SerialPorts: []exerciseModel.SerialPort{{Name: "serial0"}}}
	topo := exerciseModel.Topology{Devices: []exerciseModel.Device{author, cabled, plain}}
	pull := &exerciseModel.ToolsPull{Registry: "brain.example", Username: "u", Password: "p"}
	places := []exerciseModel.ToolsPlacement{{Pull: pull}, {}}

	images := &toolsImages{known: map[string]exerciseModel.ImageService{"brain/defender:1": {Entrypoint: []string{"/runner"}, User: "eda"}}}
	u := &ExerciseUseCase{images: images}
	out, err := u.withToolsServices(context.Background(), topo, []uuid.UUID{cabled.ID, plain.ID}, places)
	require.NoError(t, err)
	require.Equal(t, &exerciseModel.ImageService{Entrypoint: []string{"/runner"}, User: "eda"}, out.Devices[1].ImageService)
	require.Nil(t, out.Devices[2].ImageService, "a tools device without ports or files needs no service")
	require.Nil(t, out.Devices[0].ImageService, "an author's device is the image check's")
	require.Equal(t, []*exerciseModel.ToolsPull{pull}, images.pulls, "read once, with the brain's credential")
	require.Nil(t, topo.Devices[1].ImageService, "the input is not changed")

	images.known = nil
	_, err = u.withToolsServices(context.Background(), topo, []uuid.UUID{cabled.ID, plain.ID}, places)
	require.True(t, errors.Is(err, exerciseModel.ErrToolsNoService))

	_, err = (&ExerciseUseCase{}).withToolsServices(context.Background(), topo, []uuid.UUID{cabled.ID}, places)
	require.True(t, errors.Is(err, exerciseModel.ErrToolsNoService), "no image access to read it with")
}
