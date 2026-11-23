package exercise

import (
	"encoding/json"
	"strings"
	"testing"

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

func TestArchive_ReadsPortsCablesAndSeat(t *testing.T) {
	parsed, err := parseDoc(t, consoleDoc(archiveSchema, consoleTopology, seatOpponents))
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

// The writer always gives cib-exercise/1.0, with or without ports, cables and seats; what it writes reads back the same.
func TestArchive_WritesSchema10(t *testing.T) {
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

	schema, _ := write(t, consoleDoc(archiveSchema, `{"devices":[{"name":"web","type":"node"}]}`, ""))
	assert.Equal(t, archiveSchema, schema)

	schema, again := write(t, consoleDoc(archiveSchema, consoleTopology, ""))
	assert.Equal(t, archiveSchema, schema)
	topology := again.Versions[0].Variants[0].Topology
	assert.Len(t, topology.Devices[0].ConsolePorts, 2)
	assert.Len(t, topology.Devices[1].SerialPorts, 2)
	assert.True(t, topology.Connections[2].IsConsole())

	seatOnly := strings.Replace(consoleTopology, `{"kind":"console","endpoints":[{"kind":"device","device":"pc1","interface":"serial0"},{"kind":"device","device":"r1","interface":"console0"}]}`, `{"endpoints":[]}`, 1)
	schema, again = write(t, consoleDoc(archiveSchema, seatOnly, seatOpponents))
	assert.Equal(t, archiveSchema, schema)
	assert.Equal(t, "pc1", again.Versions[0].Variants[0].Opponents[0].Seat)
}

// Only cib-exercise/1.0 is read: version 1 evolves in place, so any other version string is refused.
func TestArchive_UnsupportedSchemaNames10(t *testing.T) {
	for _, other := range []string{"cib-exercise/1", "cib-exercise/1.2", "cib-exercise/2.0"} {
		err := probeArchiveSchema([]byte(`{"schema":"` + other + `"}`))
		require.ErrorIs(t, err, exerciseModel.ErrArchiveSchemaUnsupported.Err(), other)
		assert.Equal(t, archiveSchema, publicContext(t, err)["expected"])
	}
	require.NoError(t, probeArchiveSchema([]byte(`{"schema":"cib-exercise/1.0"}`)))
}
