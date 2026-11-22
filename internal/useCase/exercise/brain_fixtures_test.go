package exercise_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	exercise "github.com/cybericebox/backend/internal/useCase/exercise"
)

// The fixtures of the brain's local end-to-end runs live in <repo>/testdata/brain (see its README). The task documents there
// are written by hand; the files next to them are made by this test from the daemon's own code, so they cannot drift.
// Regenerate with:  go test ./internal/useCase/exercise -run TestBrainFixtures -update-brain-fixtures
var updateBrainFixtures = flag.Bool("update-brain-fixtures", false, "rewrite the generated files of testdata/brain")

const brainFixtures = "../../../testdata/brain"

var fixtureUUID = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// stableIDs gives every distinct uuid of a document a fixed one, in order of first appearance, so ids the daemon makes at random
// do not move a golden file.
func stableIDs(doc []byte) []byte {
	seen := map[string]string{}
	return fixtureUUID.ReplaceAllFunc(doc, func(id []byte) []byte {
		key := string(bytes.ToLower(id))
		if fixed, ok := seen[key]; ok {
			return []byte(fixed)
		}
		fixed := "00000000-0000-4000-8000-" + padHex(len(seen)+1)
		seen[key] = fixed
		return []byte(fixed)
	})
}

func padHex(n int) string {
	const digits = "0123456789abcdef"
	out := []byte("000000000000")
	for i := len(out) - 1; i >= 0 && n > 0; i-- {
		out[i] = digits[n%16]
		n /= 16
	}
	return string(out)
}

// checkGolden compares a generated JSON document with its file (or rewrites the file with -update-brain-fixtures).
func checkGolden(t *testing.T, rel string, doc any) {
	t.Helper()
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	var pretty bytes.Buffer
	require.NoError(t, json.Indent(&pretty, stableIDs(raw), "", "  "))
	pretty.WriteByte('\n')
	path := filepath.Join(brainFixtures, rel)
	if *updateBrainFixtures {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, pretty.Bytes(), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing fixture %s: run go test ./internal/useCase/exercise -run TestBrainFixtures -update-brain-fixtures", rel)
	require.Equal(t, string(want), pretty.String(), "fixture %s is out of date: run go test ./internal/useCase/exercise -run TestBrainFixtures -update-brain-fixtures", rel)
}

func brainTask(t *testing.T, name string) map[string][]byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(brainFixtures, "tasks", name, "exercise.yaml"))
	require.NoError(t, err)
	return map[string][]byte{"exercise.yaml": body}
}

func brainVariant(t *testing.T, files map[string][]byte) exerciseModel.Variant {
	t.Helper()
	archive, err := exercise.BuildArchiveNoManifest(files)
	require.NoError(t, err)
	parsed, err := exercise.ParseExerciseArchive(archive, "", exercise.DefaultLimits())
	require.NoError(t, err)
	require.Len(t, parsed.Versions, 1)
	require.Len(t, parsed.Versions[0].Variants, 1)
	return parsed.Versions[0].Variants[0]
}

// Both task documents are valid catalog imports and publishable as they are.
func TestBrainFixturesTasksImportAndPublish(t *testing.T) {
	for _, name := range []string{"web-dynamic", "opponent-lab"} {
		t.Run(name, func(t *testing.T) {
			files := brainTask(t, name)
			require.NoError(t, importExamplePublishable(t, files))
			_, written, err := importExample(t, files)
			require.NoError(t, err)
			require.Len(t, written, 1)
		})
	}
}

func TestBrainFixturesWebTaskIsDynamicWithAVPNEntryAndOneFlag(t *testing.T) {
	v := brainVariant(t, brainTask(t, "web-dynamic"))
	require.True(t, v.Topology.HasLab(), "a dynamic task has a lab")
	require.True(t, v.Topology.HasGateway(exerciseModel.DeviceTypeVPNGateway), "reached over the participant VPN")
	require.False(t, v.Topology.HasGateway(exerciseModel.DeviceTypeInternetGateway))
	require.Len(t, v.Tasks, 1)
	require.Equal(t, "find_flag", v.Tasks[0].Key)
	require.Empty(t, v.Opponents)
}

// The opponent lab: two target devices behind a switch, a manifest with an attacker (cabled at the switch) and a defender (with the
// devices it operates). Next to the document: the manifest as stored, and the lab as it is deployed for an opponent, with the tools
// device and the hidden control gateway "ctl" the platform adds.
func TestBrainFixturesOpponentLab(t *testing.T) {
	v := brainVariant(t, brainTask(t, "opponent-lab"))
	require.Len(t, v.Opponents, 2)
	require.Equal(t, exerciseModel.OpponentAttack, v.Opponents[0].Role)
	require.Equal(t, "sw1", v.Opponents[0].Foothold)
	require.Equal(t, exerciseModel.OpponentDefense, v.Opponents[1].Role)
	require.Equal(t, []string{"web", "db"}, v.Opponents[1].Operates)
	require.NoError(t, v.ValidateOpponents(), "the manifest names this variant's tasks and devices")
	checkGolden(t, "tasks/opponent-lab/opponents.json", v.Opponents)

	// The attacker's tools device sits at the foothold; the defender's with the devices it operates.
	attacker, tools, err := v.Topology.WithToolsDevice("registry.example/cib-tools:1", exerciseModel.ToolsPlacement{
		Foothold: v.Opponents[0].Foothold,
		Env:      exerciseModel.EDAEnv("00000000-0000-4000-8000-0000000000aa", "-----BEGIN CERTIFICATE-----\n<the brain's enrollment ca_pem>\n-----END CERTIFICATE-----\n"),
	})
	require.NoError(t, err)
	require.Equal(t, exerciseModel.ToolsDeviceName, tools.Name)
	require.True(t, attacker.HasGateway(exerciseModel.DeviceTypeVPNGateway))
	require.Contains(t, deviceNames(attacker), exerciseModel.ControlGatewayName, "the control gateway carries the brain's control of the tools device")
	checkGolden(t, "tasks/opponent-lab/deployed-topology-attack.json", attacker)

	defender, _, err := v.Topology.WithToolsDevice("registry.example/cib-tools:1", exerciseModel.ToolsPlacement{
		Beside: v.Opponents[1].Operates,
		Env:    exerciseModel.EDAEnv("00000000-0000-4000-8000-0000000000bb", ""),
	})
	require.NoError(t, err)
	checkGolden(t, "tasks/opponent-lab/deployed-topology-defense.json", defender)

	// Both opponents together in one lab: a tools device each, one control gateway; the attacker's device has index 0 (host .10),
	// the defender's index 1 (host .11).
	both, devices, err := v.Topology.WithToolsDevices([]exerciseModel.ToolsPlacement{
		{Image: "registry.example/cib-tools:1", Foothold: v.Opponents[0].Foothold, Env: exerciseModel.EDAEnv("00000000-0000-4000-8000-0000000000aa", "")},
		{Image: "registry.example/cib-tools:1", Beside: v.Opponents[1].Operates, Env: exerciseModel.EDAEnv("00000000-0000-4000-8000-0000000000bb", "")},
	})
	require.NoError(t, err)
	require.Len(t, devices, 2)
	require.Equal(t, []string{"tools", "tools-2"}, []string{devices[0].Name, devices[1].Name})
	require.Equal(t, exerciseModel.ControlHost(1), devices[1].Interfaces[1].IP.AddressRef.Host)
	checkGolden(t, "tasks/opponent-lab/deployed-topology-both.json", both)
}

func deviceNames(t exerciseModel.Topology) []string {
	names := make([]string, 0, len(t.Devices))
	for _, d := range t.Devices {
		names = append(names, d.Name)
	}
	return names
}
