package exercise

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

const consoleTopology = `{"devices":[
 {"name":"r1","type":"node","interfaces":[{"name":"eth0","ip":{"type":"dhcp"}}],
  "console_ports":[{"name":"console0","command":["/usr/bin/vtysh"]},{"name":"aux0","native":{"kind":"tcp","port":2001}}]},
 {"name":"pc1","type":"node","interfaces":[{"name":"eth0","ip":{"type":"dhcp"}}],"serial_ports":[{"name":"serial0"},{"name":"serial1","path":"/dev/ttyUSB0"}]},
 {"name":"lan","type":"unmanaged-switch"}],
 "connections":[
 {"endpoints":[{"kind":"device","device":"pc1","interface":"eth0"},{"kind":"device","device":"lan","interface":"GigabitEthernet0/1"}]},
 {"endpoints":[{"kind":"device","device":"r1","interface":"eth0"},{"kind":"device","device":"lan","interface":"GigabitEthernet0/2"}]},
 {"kind":"console","endpoints":[{"kind":"device","device":"pc1","interface":"serial0"},{"kind":"device","device":"r1","interface":"console0"}]}]}`

func consoleDoc(schema, topology, opponents string) string {
	variant := `{"tasks":[{"key":"harden","name":"t"}],"topology":` + topology
	if opponents != "" {
		variant += `,"opponents":` + opponents
	}
	return `{"schema":"` + schema + `","exercise":{"name":"x"},"versions":[{"status":"draft","variants":[` + variant + `}]}]}`
}

const seatOpponents = `[{"role":"defense","band":2,"goal":["harden"],"operates":["r1"],"seat":"pc1"}]`

func TestArchive11_ReadsPortsCablesAndSeat(t *testing.T) {
	parsed, err := parseDoc(t, consoleDoc(archiveSchema11, consoleTopology, seatOpponents))
	require.NoError(t, err)
	variant := parsed.Versions[0].Variants[0]
	r1, pc1 := variant.Topology.Devices[0], variant.Topology.Devices[1]
	assert.Equal(t, []exerciseModel.ConsolePort{
		{Name: "console0", Command: []string{"/usr/bin/vtysh"}},
		{Name: "aux0", Native: &exerciseModel.NativeSource{Kind: exerciseModel.NativeTCP, Port: 2001}},
	}, r1.ConsolePorts)
	assert.Equal(t, []exerciseModel.SerialPort{{Name: "serial0"}, {Name: "serial1", Path: "/dev/ttyUSB0"}}, pc1.SerialPorts)
	cable := variant.Topology.Connections[2]
	assert.True(t, cable.IsConsole())
	assert.Equal(t, pc1.ID, cable.Endpoints[0].DeviceID)
	assert.Equal(t, "console0", cable.Endpoints[1].Interface)
	assert.False(t, variant.Topology.Connections[0].IsConsole())
	assert.Equal(t, "pc1", variant.Opponents[0].Seat)
}

// A 1.0 document never holds a 1.1 key: the key is named, with the schema that has it.
func TestArchive10_RefusesThe11KeysByName(t *testing.T) {
	for _, tc := range []struct{ name, doc, key string }{
		{"console_ports", consoleDoc(archiveSchema10, `{"devices":[{"name":"r1","type":"node","console_ports":[{"name":"console0"}]}]}`, ""), "console_ports"},
		{"serial_ports", consoleDoc(archiveSchema10, `{"devices":[{"name":"pc1","type":"node","serial_ports":[{"name":"serial0"}]}]}`, ""), "serial_ports"},
		{"connection kind", consoleDoc(archiveSchema10, `{"devices":[{"name":"a","type":"node"}],"connections":[{"kind":"console","endpoints":[]}]}`, ""), "kind"},
		{"opponent seat", consoleDoc(archiveSchema10, `{"devices":[{"name":"r1","type":"node"}]}`, seatOpponents), "seat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeArchiveV2([]byte(tc.doc))
			require.ErrorIs(t, err, exerciseModel.ErrArchiveInvalid.Err())
			reason := publicContext(t, err)["reason"].(string)
			assert.Contains(t, reason, `unknown key "`+tc.key+`"`)
			assert.Contains(t, reason, archiveSchema11)
		})
	}
}

func TestArchive_BothSchemasReadADocumentWithoutTheNewKeys(t *testing.T) {
	for _, schema := range []string{archiveSchema10, archiveSchema11} {
		_, err := parseDoc(t, consoleDoc(schema, `{"devices":[{"name":"web","type":"node"}]}`, ""))
		require.NoError(t, err, schema)
	}
}

// The writer gives the lowest version that holds the content: 1.0 without ports, cables or seats, else 1.1; and what it
// writes reads back the same.
func TestArchive_WritesTheLowestSchema(t *testing.T) {
	write := func(t *testing.T, doc string) (string, ParsedExerciseArchive) {
		t.Helper()
		parsed, err := parseDoc(t, doc)
		require.NoError(t, err)
		versions := []exerciseModel.ExerciseVersion{{Status: exerciseModel.VersionStatusDraft, Name: "x", Variants: parsed.Versions[0].Variants}}
		data, err := buildV2Document(exerciseModel.Exercise{Name: "x"}, versions, nil)
		require.NoError(t, err)
		var head struct {
			Schema string `json:"schema"`
		}
		require.NoError(t, json.Unmarshal(data, &head))
		again, err := parseDoc(t, string(data))
		require.NoError(t, err, "the written document reads back")
		return head.Schema, again
	}

	schema, _ := write(t, consoleDoc(archiveSchema11, `{"devices":[{"name":"web","type":"node"}]}`, ""))
	assert.Equal(t, archiveSchema10, schema, "nothing of 1.1 is used")

	schema, again := write(t, consoleDoc(archiveSchema11, consoleTopology, ""))
	assert.Equal(t, archiveSchema11, schema)
	topology := again.Versions[0].Variants[0].Topology
	assert.Len(t, topology.Devices[0].ConsolePorts, 2)
	assert.Len(t, topology.Devices[1].SerialPorts, 2)
	assert.True(t, topology.Connections[2].IsConsole())

	seatOnly := strings.Replace(consoleTopology, `{"kind":"console","endpoints":[{"kind":"device","device":"pc1","interface":"serial0"},{"kind":"device","device":"r1","interface":"console0"}]}`, `{"endpoints":[]}`, 1)
	schema, again = write(t, consoleDoc(archiveSchema11, seatOnly, seatOpponents))
	assert.Equal(t, archiveSchema11, schema)
	assert.Equal(t, "pc1", again.Versions[0].Variants[0].Opponents[0].Seat)
}

func TestArchive_UnsupportedSchemaNamesBoth(t *testing.T) {
	err := probeArchiveSchema([]byte(`{"schema":"cib-exercise/1.2"}`))
	require.ErrorIs(t, err, exerciseModel.ErrArchiveSchemaUnsupported.Err())
	assert.Equal(t, "cib-exercise/1.0, cib-exercise/1.1", publicContext(t, err)["expected"])
	require.NoError(t, probeArchiveSchema([]byte(`{"schema":"cib-exercise/1.1"}`)))
	require.NoError(t, probeArchiveSchema([]byte(`{"schema":"cib-exercise/1.0"}`)))
}

// A linked variant writes neither its topology nor its opponents (it inherits them), so its own seat never makes the export
// 1.1.
func TestArchive_LinkedVariantsOwnSeatDoesNotCount(t *testing.T) {
	base := exerciseModel.Variant{ID: freshID(), Tasks: []exerciseModel.Task{{Key: "k", Name: "t"}}}
	linked := exerciseModel.Variant{ID: freshID(), Tasks: []exerciseModel.Task{{Key: "k", Name: "t"}},
		LinkedTo:  uuid.NullUUID{UUID: base.ID, Valid: true},
		Opponents: []exerciseModel.Opponent{{Role: exerciseModel.OpponentDefense, Seat: "pc1"}}}
	data, err := buildV2Document(exerciseModel.Exercise{Name: "x"}, []exerciseModel.ExerciseVersion{{Name: "x", Variants: []exerciseModel.Variant{base, linked}}}, nil)
	require.NoError(t, err)
	var head struct {
		Schema   string `json:"schema"`
		Versions []struct {
			Variants []json.RawMessage `json:"variants"`
		} `json:"versions"`
	}
	require.NoError(t, json.Unmarshal(data, &head))
	require.Contains(t, string(head.Versions[0].Variants[1]), "linked_to", "the second variant is written as linked")
	assert.Equal(t, archiveSchema10, head.Schema)
}
