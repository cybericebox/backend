package exercise

import (
	"testing"

	"github.com/gofrs/uuid"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

// The kernel log flag goes through the editor's DTO both ways, so a save never drops it.
func TestTopologyDTOCarriesTheKernelLogBothWays(t *testing.T) {
	r1 := exerciseModel.Device{ID: uuid.Must(uuid.NewV7()), Name: "r1", Type: exerciseModel.DeviceTypeNode, Profile: exerciseModel.NodeProfileMicroVM,
		ConsolePorts: []exerciseModel.ConsolePort{{Name: "console0"}}, KernelLog: true}
	plain := exerciseModel.Device{ID: uuid.Must(uuid.NewV7()), Name: "web", Type: exerciseModel.DeviceTypeNode}
	dto := topologyToDTO(exerciseModel.Topology{Devices: []exerciseModel.Device{r1, plain}})
	if !dto.Devices[0].KernelLog || dto.Devices[1].KernelLog {
		t.Fatalf("dto: %+v", dto.Devices)
	}
	out := dto.toDomain()
	if !out.Devices[0].KernelLog || out.Devices[1].KernelLog {
		t.Fatalf("domain: %+v", out.Devices)
	}
}
