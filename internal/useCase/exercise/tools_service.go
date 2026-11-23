package exercise

import (
	"context"

	"github.com/gofrs/uuid"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

// toolsServiceReader reads the start configuration (entrypoint, cmd, user, working directory) of a tools image, with the pull
// credential the brain gave for it when there is one. Nothing is stored: the credential is the run's.
type toolsServiceReader interface {
	ToolsImageService(ctx context.Context, image string, pull *exerciseModel.ToolsPull) (exerciseModel.ImageService, bool)
}

// withToolsServices gives every tools device that starts through the launcher (it has serial ports from auto-cabling, or files)
// the start configuration of its image, so the laboratory can be told its service command and user. The brain's image is hidden
// from the image check, so it has none yet; the platform's fallback image may have one from the check. A configuration that
// cannot be read refuses the deploy with ErrToolsNoService: the laboratory would refuse such a device.
func (u *ExerciseUseCase) withToolsServices(ctx context.Context, topo exerciseModel.Topology, ids []uuid.UUID, places []exerciseModel.ToolsPlacement) (exerciseModel.Topology, error) {
	if len(ids) == 0 {
		return topo, nil
	}
	pulls := make(map[uuid.UUID]*exerciseModel.ToolsPull, len(ids))
	for i, id := range ids {
		if i < len(places) {
			pulls[id] = places[i].Pull
		}
	}
	reader, _ := u.images.(toolsServiceReader)
	out := topo
	out.Devices = append([]exerciseModel.Device(nil), topo.Devices...)
	for i, d := range out.Devices {
		pull, isTools := pulls[d.ID]
		if !isTools || !d.NeedsServiceCommand() || d.ImageService != nil {
			continue
		}
		if reader == nil {
			return exerciseModel.Topology{}, exerciseModel.ErrToolsNoService
		}
		service, known := reader.ToolsImageService(ctx, d.Image, pull)
		if !known {
			return exerciseModel.Topology{}, exerciseModel.ErrToolsNoService
		}
		out.Devices[i].ImageService = &service
	}
	return out, nil
}
