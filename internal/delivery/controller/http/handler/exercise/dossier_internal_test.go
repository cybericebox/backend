package exercise

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	exerciseUseCase "github.com/cybericebox/backend/internal/useCase/exercise"
)

func TestDossierResponse(t *testing.T) {
	out := dossierToResponse(exerciseUseCase.DossierView{Declared: true, Dossier: exerciseModel.Dossier{
		Links:     []exerciseModel.ConsoleLink{{SerialPort: "serial0", Path: "/dev/ttyCON0", Device: "r1", ConsolePort: "console0", Kind: exerciseModel.ConsoleKindProgram, Program: "vtysh"}},
		Forbidden: []string{"core-fw"}, Credentials: []exerciseModel.DefenderCredential{{Device: "r1", Port: "console0", User: "admin", Value: "s3cret"}},
		Topology: exerciseModel.DefenderTopologyOperates, Operates: []string{"r1"},
	}})
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	require.JSONEq(t, `{"Declared":true,"Topology":"operates","Operates":["r1"],"Forbidden":["core-fw"],
		"Links":[{"SerialPort":"serial0","Path":"/dev/ttyCON0","Device":"r1","ConsolePort":"console0","ConsoleKind":"program","Program":"vtysh"}],
		"Credentials":[{"Device":"r1","Port":"console0","User":"admin","Value":"s3cret"}]}`, string(raw))

	raw, err = json.Marshal(dossierToResponse(exerciseUseCase.DossierView{}))
	require.NoError(t, err)
	require.JSONEq(t, `{"Declared":false,"Topology":"","Operates":[],"Forbidden":[],"Links":[],"Credentials":[]}`, string(raw))
}

// A secret credential never goes back to the editor; a plain one does; a blank secret in a request is kept by the save.
func TestDefenderDTO(t *testing.T) {
	d := defenderToDTO(&exerciseModel.Defender{Seat: "pc1", Credentials: []exerciseModel.DefenderCredential{
		{Device: "r1", User: "admin", Value: "ciphertext", Secret: true}, {Device: "web", Value: "plain"},
	}})
	require.Empty(t, d.Credentials[0].Value)
	require.Equal(t, "plain", d.Credentials[1].Value)
	back := d.toDomain()
	require.Equal(t, "pc1", back.Seat)
	require.True(t, back.Credentials[0].Secret)
	require.Nil(t, (*defenderDTO)(nil).toDomain())
	require.Nil(t, defenderToDTO(nil))
}
