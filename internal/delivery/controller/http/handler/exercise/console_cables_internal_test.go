package exercise

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	infraModel "github.com/cybericebox/backend/internal/model/infrastructure"
	exerciseUseCase "github.com/cybericebox/backend/internal/useCase/exercise"
)

// The test-lab status carries one entry per console line: its two ends, whether it is up and why it is down.
func TestDeployStatusCarriesTheConsoleCables(t *testing.T) {
	out := deployStatusToResponse(exerciseModel.LabDeployStatus{ConsoleCables: []exerciseModel.LabConsoleCable{
		{SerialDevice: "pc1", SerialPort: "serial0", ConsoleDevice: "r1", ConsolePort: "console0", LineUp: true},
		{SerialDevice: "pc1", SerialPort: "serial1", ConsoleDevice: "sw", ConsolePort: "aux0", Reason: exerciseModel.ConsoleCableWaitingForDevice},
	}})
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, []any{
		map[string]any{"Serial": map[string]any{"Device": "pc1", "Port": "serial0"}, "Console": map[string]any{"Device": "r1", "Port": "console0"}, "LineUp": true, "Reason": ""},
		map[string]any{"Serial": map[string]any{"Device": "pc1", "Port": "serial1"}, "Console": map[string]any{"Device": "sw", "Port": "aux0"}, "LineUp": false, "Reason": "WaitingForDevice"},
	}, got["ConsoleCables"])

	raw, err = json.Marshal(deployStatusToResponse(exerciseModel.LabDeployStatus{}))
	require.NoError(t, err)
	require.NotContains(t, string(raw), "ConsoleCables", "absent for a lab without cables")
}

func TestLabCapabilitiesCarryTheConsoleCables(t *testing.T) {
	view := exerciseUseCase.LabCapabilitiesView{LabCapabilities: infraModel.LabCapabilities{ConsoleCables: infraModel.AvailabilitySome}}
	require.Equal(t, "some", toLabCapabilities(view).ConsoleCables)
}
