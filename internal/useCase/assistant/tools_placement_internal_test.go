package assistant

import (
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

// A role that operates hands its devices and its seat to the placement (the tools device is cabled from them); an attacker
// hands neither.
func TestToolsPlacementCarriesTheSeatAndTheOperatedDevices(t *testing.T) {
	run := uuid.Must(uuid.NewV7())
	defense := toolsPlacement(exerciseModel.Opponent{Role: exerciseModel.OpponentDefense, Operates: []string{"r1", "sw"}, Seat: "pc1"}, run, "")
	require.Equal(t, []string{"r1", "sw"}, defense.Beside)
	require.Equal(t, []string{"r1", "sw"}, defense.Operates)
	require.Equal(t, "pc1", defense.Seat)

	both := toolsPlacement(exerciseModel.Opponent{Role: exerciseModel.OpponentAttackDefense, Operates: []string{"r1"}, Foothold: "lan"}, run, "")
	require.Equal(t, "lan", both.Foothold)
	require.Equal(t, []string{"r1"}, both.Operates)
	require.Empty(t, both.Seat)

	attack := toolsPlacement(exerciseModel.Opponent{Role: exerciseModel.OpponentAttack, Foothold: "lan"}, run, "")
	require.Empty(t, attack.Operates)
	require.Empty(t, attack.Seat)
}
