package exercise

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

func defenderDoc(schema, defender string) string {
	variant := `{"tasks":[{"key":"harden","name":"t"}],"topology":` + consoleTopology + `,"defender":` + defender
	return `{"schema":"` + schema + `","exercise":{"name":"x"},"versions":[{"status":"draft","variants":[` + variant + `}]}]}`
}

const plainDefender = `{"seat":"pc1","topology":"none","off_limits":["lan"],"credentials":[{"device":"r1","port":"console0","user":"admin","value":"cisco"}]}`

// The defender block is a key of 1.1: read, validated, written back (and a 1.0 document that holds it is refused by name).
func TestArchive11_DefenderBlock(t *testing.T) {
	parsed, err := parseDoc(t, defenderDoc(archiveSchema11, plainDefender))
	require.NoError(t, err)
	assert.Equal(t, &exerciseModel.Defender{
		Seat: "pc1", Topology: exerciseModel.DefenderTopologyNone, OffLimits: []string{"lan"},
		Credentials: []exerciseModel.DefenderCredential{{Device: "r1", Port: "console0", User: "admin", Value: "cisco"}},
	}, parsed.Versions[0].Variants[0].Defender)

	versions := []exerciseModel.ExerciseVersion{{Status: exerciseModel.VersionStatusDraft, Name: "x", Variants: parsed.Versions[0].Variants}}
	data, err := buildV2Document(exerciseModel.Exercise{Name: "x"}, versions, nil)
	require.NoError(t, err)
	var head struct {
		Schema string `json:"schema"`
	}
	require.NoError(t, json.Unmarshal(data, &head))
	assert.Equal(t, archiveSchema11, head.Schema, "a defender block needs 1.1")
	again, err := parseDoc(t, string(data))
	require.NoError(t, err)
	assert.Equal(t, parsed.Versions[0].Variants[0].Defender, again.Versions[0].Variants[0].Defender)

	_, err = decodeArchiveV2([]byte(defenderDoc(archiveSchema10, plainDefender)))
	require.ErrorIs(t, err, exerciseModel.ErrArchiveInvalid.Err())
	assert.Contains(t, publicContext(t, err)["reason"].(string), `unknown key "defender"`)

	_, err = parseDoc(t, defenderDoc(archiveSchema11, `{"seat":"r1"}`))
	require.ErrorIs(t, err, exerciseModel.ErrDefenderInvalid.Err(), "the seat has no serial port")
}
