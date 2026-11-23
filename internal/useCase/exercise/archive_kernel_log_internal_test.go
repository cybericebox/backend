package exercise

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

// kernel_log is a device key of cib-exercise/1.0: the import reads it, the export writes it (only when on), and what is written
// reads back the same.
func TestArchive_KernelLogReadAndWritten(t *testing.T) {
	topology := strings.Replace(consoleTopology, `{"name":"r1","type":"node",`, `{"name":"r1","type":"node","profile":"microvm","kernel_log":true,`, 1)
	require.NotEqual(t, consoleTopology, topology)
	parsed, err := parseDoc(t, consoleDoc(archiveSchema, topology, ""))
	require.NoError(t, err)
	devices := parsed.Versions[0].Variants[0].Topology.Devices
	assert.True(t, devices[0].KernelLog)
	assert.False(t, devices[1].KernelLog)

	versions := []exerciseModel.ExerciseVersion{{Status: exerciseModel.VersionStatusDraft, Name: "x", Variants: parsed.Versions[0].Variants}}
	data, err := buildV2Document(exerciseModel.Exercise{Name: "x"}, versions, nil)
	require.NoError(t, err)
	var doc struct {
		Versions []struct {
			Variants []struct {
				Topology struct {
					Devices []map[string]any `json:"devices"`
				} `json:"topology"`
			} `json:"variants"`
		} `json:"versions"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))
	written := doc.Versions[0].Variants[0].Topology.Devices
	assert.Equal(t, true, written[0]["kernel_log"])
	assert.NotContains(t, written[1], "kernel_log", "the default is not written")

	again, err := parseDoc(t, string(data))
	require.NoError(t, err)
	assert.True(t, again.Versions[0].Variants[0].Topology.Devices[0].KernelLog)
}
