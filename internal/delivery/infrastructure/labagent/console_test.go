package labagent

import (
	"testing"

	"github.com/stretchr/testify/require"

	eventLabModel "github.com/cybericebox/backend/internal/model/eventLab"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	infraModel "github.com/cybericebox/backend/internal/model/infrastructure"
)

// Until the agent client reads the console feature of the agents (the deploy phase of console cables), no agent is known to
// offer it: a lab with a console port or cable is refused, one without is not.
func TestRequireConsoleCables(t *testing.T) {
	require.NoError(t, requireConsoleCables(nodeOf("")))
	withPort := nodeOf("")
	withPort.Devices[0].SerialPorts = []exerciseModel.SerialPort{{Name: "serial0"}}
	require.ErrorIs(t, requireConsoleCables(withPort), exerciseModel.ErrConsoleUnavailable.Err())
	withCable := exerciseModel.Topology{Connections: []exerciseModel.Connection{{Kind: exerciseModel.ConnectionConsole}}}
	require.ErrorIs(t, requireConsoleCables(withCable), exerciseModel.ErrConsoleUnavailable.Err())
}

func TestDeployLabRefusesConsoleBeforeAnyAgentWork(t *testing.T) {
	topo := nodeOf("")
	topo.Devices[0].ConsolePorts = []exerciseModel.ConsolePort{{Name: "console0"}}
	err := NewFleet(nil, nil).DeployLab(t.Context(), "g", "l", infraModel.LabMeta{}, topo)
	require.ErrorIs(t, err, exerciseModel.ErrConsoleUnavailable.Err())
}

func TestConsoleLinesOfTheFleet(t *testing.T) {
	f := NewFleet(nil, nil)
	require.False(t, f.ConsoleCablesAvailable())
	topo := nodeOf("")
	require.NoError(t, f.RequireConsoleLines(topo))
	topo.Devices[0].SerialPorts = []exerciseModel.SerialPort{{Name: "serial0"}}
	require.ErrorIs(t, f.RequireConsoleLines(topo), exerciseModel.ErrConsoleUnavailable.Err())
	for _, mode := range []string{"", "required"} {
		require.ErrorIs(t, f.RequireLabLifecyclePreparation(t.Context(), "g", eventLabModel.Policy{SnapshotMode: mode}, topo), exerciseModel.ErrConsoleUnavailable.Err(), mode)
	}
}
