package infrastructure

import (
	"testing"

	"github.com/stretchr/testify/require"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

func withConsole(f *AgentFeatures, enabled bool, maxCables int32) *AgentFeatures {
	f.ConsoleCables = &ConsoleCablesFeature{Enabled: enabled, MaxCablesPerLab: maxCables}
	return f
}

// A lab with console lines needs a laboratory that serves them and takes its cables; a lab without them needs nothing.
func TestRequireConsoleLines(t *testing.T) {
	lines := exerciseModel.LabNeeds{ConsoleLines: true, ConsoleCables: 3}
	require.NoError(t, AgentFeatures{}.RequireConsoleLines(exerciseModel.LabNeeds{}))
	require.ErrorIs(t, AgentFeatures{}.RequireConsoleLines(lines), exerciseModel.ErrConsoleUnavailable.Err(), "an agent older than the field")
	require.ErrorIs(t, withConsole(full(), false, 64).RequireConsoleLines(lines), exerciseModel.ErrConsoleUnavailable.Err(), "switched off")
	require.NoError(t, withConsole(full(), true, 64).RequireConsoleLines(lines))
	require.NoError(t, withConsole(full(), true, 0).RequireConsoleLines(lines), "no limit reported")
	err := withConsole(full(), true, 2).RequireConsoleLines(lines)
	require.ErrorIs(t, err, exerciseModel.ErrConsoleCableLimit.Err())
	var coded interface{ PublicContext() map[string]any }
	require.ErrorAs(t, err, &coded)
	require.EqualValues(t, 2, coded.PublicContext()["limit"])
}

func TestCapabilitiesOfConsoleCables(t *testing.T) {
	require.Equal(t, AvailabilityUnknown, CapabilitiesOf(nil).ConsoleCables)
	require.Equal(t, AvailabilityNone, CapabilitiesOf([]*AgentFeatures{full(), old()}).ConsoleCables)
	require.Equal(t, AvailabilitySome, CapabilitiesOf([]*AgentFeatures{withConsole(full(), true, 64), old()}).ConsoleCables)
	require.Equal(t, AvailabilityAll, CapabilitiesOf([]*AgentFeatures{withConsole(full(), true, 64), withConsole(old(), true, 64)}).ConsoleCables)
}

func TestConsoleCablesUnavailableIsAPlanningError(t *testing.T) {
	err := ReasonError(ReasonConsoleCablesUnavailable)
	require.ErrorIs(t, err, exerciseModel.ErrConsoleUnavailable.Err())
	require.True(t, IsPlanningError(err))
}
