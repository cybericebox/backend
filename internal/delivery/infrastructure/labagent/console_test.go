package labagent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	labclient "github.com/cybericebox/laboratory/pkg/agent/client"
	labpb "github.com/cybericebox/laboratory/pkg/agent/protobuf"

	eventLabModel "github.com/cybericebox/backend/internal/model/eventLab"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	infraModel "github.com/cybericebox/backend/internal/model/infrastructure"
)

// cabledLab is a PC with two serial ports cabled to the console port of a router; the cable lists the console end first.
func cabledLab() exerciseModel.Topology {
	pc, r1 := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	return exerciseModel.Topology{
		Devices: []exerciseModel.Device{
			{
				ID: pc, Name: "pc1", Type: exerciseModel.DeviceTypeNode, Image: "tools",
				ImageService: &exerciseModel.ImageService{Entrypoint: []string{"/bin/sleep"}, Cmd: []string{"infinity"}},
				SerialPorts:  []exerciseModel.SerialPort{{Name: "serial0"}, {Name: "serial1", Path: "/dev/ttyUSB1"}},
			},
			{
				ID: r1, Name: "r1", Type: exerciseModel.DeviceTypeNode, Image: "frr",
				ImageService: &exerciseModel.ImageService{Entrypoint: []string{"/start"}, User: "frr"},
				ConsolePorts: []exerciseModel.ConsolePort{
					{Name: "console0", Command: []string{"/usr/bin/vtysh"}, User: "frr"},
					{Name: "aux0"},
					{Name: "console1", Native: &exerciseModel.NativeSource{Kind: exerciseModel.NativeTCP, Port: 2001}},
				},
			},
		},
		Connections: []exerciseModel.Connection{{Kind: exerciseModel.ConnectionConsole, Endpoints: []exerciseModel.Endpoint{
			{Kind: exerciseModel.EndpointDevice, DeviceID: r1, Interface: "console0"},
			{Kind: exerciseModel.EndpointDevice, DeviceID: pc, Interface: "serial0"},
		}}},
	}
}

func consoleOffered(maxCables int32) *infraModel.AgentFeatures {
	f := allOptions()
	f.ContractVersion = "3.1"
	f.ConsoleCables = &infraModel.ConsoleCablesFeature{Enabled: true, MaxCablesPerLab: maxCables, MaxBytesPerSecond: 65536}
	return f
}

func TestFeaturesOfReadsTheConsoleCablesOfContract31(t *testing.T) {
	got := FeaturesOf(&labpb.FeaturesResponse{ConsoleCables: &labpb.ConsoleCablesFeature{Enabled: true, MaxCablesPerLab: 64, MaxBytesPerSecond: 65536}})
	require.Equal(t, &infraModel.ConsoleCablesFeature{Enabled: true, MaxCablesPerLab: 64, MaxBytesPerSecond: 65536}, got.ConsoleCables)
	require.True(t, got.ServesConsoleCables())
	old := FeaturesOf(&labpb.FeaturesResponse{})
	require.Nil(t, old.ConsoleCables, "an agent older than the field reports none")
	require.False(t, old.ServesConsoleCables())
}

// The capability of the editor and the event pre-flight follow the reports of the agents that are used.
func TestConsoleLinesOfTheFleet(t *testing.T) {
	topo := cabledLab()
	empty := NewFleet(nil, nil)
	require.False(t, empty.ConsoleCablesAvailable())
	require.NoError(t, empty.RequireConsoleLines(nodeOf("")), "a lab without console lines needs nothing")
	require.ErrorIs(t, empty.RequireConsoleLines(topo), exerciseModel.ErrConsoleUnavailable.Err())

	off := &Member{Enabled: true, Features: NewFeatureCell(allOptions())}
	unreported := &Member{Enabled: true, Features: NewFeatureCell(nil)}
	f := NewFleet(nil, nil, off, unreported)
	require.False(t, f.ConsoleCablesAvailable(), "neither an agent without the feature nor one that has not reported offers it")
	require.ErrorIs(t, f.RequireConsoleLines(topo), exerciseModel.ErrConsoleUnavailable.Err())
	for _, mode := range []string{"", "required"} {
		require.ErrorIs(t, f.RequireLabLifecyclePreparation(t.Context(), "g", eventLabModel.Policy{SnapshotMode: mode}, topo), exerciseModel.ErrConsoleUnavailable.Err(), mode)
	}

	on := &Member{Enabled: true, Features: NewFeatureCell(consoleOffered(64))}
	f = NewFleet(nil, nil, off, on)
	require.True(t, f.ConsoleCablesAvailable())
	require.NoError(t, f.RequireConsoleLines(topo))

	disabled := &Member{Enabled: false, Features: NewFeatureCell(consoleOffered(64))}
	require.False(t, NewFleet(nil, nil, off, disabled).ConsoleCablesAvailable(), "a disabled agent is not used")

	small := &Member{Enabled: true, Features: NewFeatureCell(consoleOffered(0))}
	small.Features.Get().ConsoleCables.MaxCablesPerLab = 1
	two := cabledLab()
	two.Connections = append(two.Connections, exerciseModel.Connection{Kind: exerciseModel.ConnectionConsole, Endpoints: []exerciseModel.Endpoint{
		{Kind: exerciseModel.EndpointDevice, DeviceID: two.Devices[0].ID, Interface: "serial1"},
		{Kind: exerciseModel.EndpointDevice, DeviceID: two.Devices[1].ID, Interface: "aux0"},
	}})
	err := NewFleet(nil, nil, off, small).RequireConsoleLines(two)
	require.ErrorIs(t, err, exerciseModel.ErrConsoleCableLimit.Err(), "a limit is named rather than the absence")
}

func TestDeployLabRefusesConsoleBeforeAnyAgentWork(t *testing.T) {
	err := NewFleet(nil, nil).DeployLab(t.Context(), "g", "l", infraModel.LabMeta{}, cabledLab())
	require.ErrorIs(t, err, exerciseModel.ErrConsoleUnavailable.Err())
}

// The agent a lab goes to must report console lines itself: an agent that has not reported is refused (an agent older than
// contract 3.1 would drop the ports and cables from the lab), and a report switched on later is read at the refusal.
func TestDeployLabNeedsAnAgentThatServesConsoleLines(t *testing.T) {
	ctx := context.Background()
	t.Run("offered", func(t *testing.T) {
		f := newFleetFixture(t)
		f.am.Features = NewFeatureCell(consoleOffered(64))
		require.NoError(t, f.fleet.DeployLab(ctx, "e-1-t-1", "c-1", infraModel.LabMeta{}, cabledLab()))
		spec := decode(t, f.a.createLabs.GetVariants()[0].GetSpecJson())
		require.Len(t, spec.ConsoleCables, 1)
		require.Empty(t, spec.Connections, "a console cable is never a network connection")
	})
	t.Run("a group goes to an agent that serves them", func(t *testing.T) {
		f := newFleetFixture(t)
		f.am.Features = NewFeatureCell(allOptions())
		f.bm.Features = NewFeatureCell(consoleOffered(64))
		require.NoError(t, f.fleet.DeployLab(ctx, "e-1-t-9", "c-1", infraModel.LabMeta{}, cabledLab()))
		require.Nil(t, f.a.createLabs, "the agent without console lines gets nothing")
		require.NotNil(t, f.b.createLabs)
	})
	for name, enabled := range map[string]bool{"switched on after the report": true, "still off": false} {
		t.Run(name, func(t *testing.T) {
			f := newFleetFixture(t)
			require.NoError(t, f.fleet.EnsureVPNGroup(ctx, "e-1-t-1"), "the group lives on agent a")
			f.am.Features = NewFeatureCell(allOptions())
			f.bm.Features = NewFeatureCell(consoleOffered(64))
			f.a.features = &labpb.FeaturesResponse{ContractVersion: "3.1", ConsoleCables: &labpb.ConsoleCablesFeature{Enabled: enabled, MaxCablesPerLab: 64}}
			err := f.fleet.DeployLab(ctx, "e-1-t-1", "c-1", infraModel.LabMeta{}, cabledLab())
			if enabled {
				require.NoError(t, err)
				require.NotNil(t, f.a.createLabs)
				return
			}
			require.ErrorIs(t, err, exerciseModel.ErrConsoleUnavailable.Err())
			require.Nil(t, f.a.createLabs, "nothing is created")
		})
	}
	t.Run("not reported yet", func(t *testing.T) {
		f := newFleetFixture(t)
		require.NoError(t, f.fleet.EnsureVPNGroup(ctx, "e-1-t-1"))
		f.bm.Features = NewFeatureCell(consoleOffered(64))
		err := f.fleet.DeployLab(ctx, "e-1-t-1", "c-1", infraModel.LabMeta{}, cabledLab())
		require.ErrorIs(t, err, exerciseModel.ErrConsoleUnavailable.Err(), "an agent that has not reported is refused for console lines")
		require.Nil(t, f.a.createLabs)
	})
}

// The agent's refusals of console lines map onto the platform's errors.
func TestConsoleRefusalsOfTheAgent(t *testing.T) {
	require.ErrorIs(t, infraModel.ReasonError(labclient.ReasonConsoleCablesUnavailable), exerciseModel.ErrConsoleUnavailable.Err())
	require.True(t, infraModel.IsPlanningError(infraModel.ReasonError(labclient.ReasonConsoleCablesUnavailable)), "never retried as it is")

	st, err := status.New(codes.InvalidArgument, "variant \"v\": ConsoleCableInvalid: device \"r1\": the port \"console0\" is used by more than one console cable").
		WithDetails(&errdetails.ErrorInfo{Reason: labclient.ReasonConsoleCableInvalid, Domain: labclient.ErrorDomain})
	require.NoError(t, err)
	mapped := agentErr("create lab", st.Err())
	require.ErrorIs(t, mapped, exerciseModel.ErrConsoleSpecRefused.Err())
	require.Contains(t, mapped.Error(), "console0", "the agent's words stay in the log")

	other, err := status.New(codes.InvalidArgument, "bad").WithDetails(&errdetails.ErrorInfo{Reason: "OTHER", Domain: labclient.ErrorDomain})
	require.NoError(t, err)
	require.NotErrorIs(t, agentErr("create lab", other.Err()), exerciseModel.ErrConsoleSpecRefused.Err())
}

// The lab status carries the console lines, with a reason of the fixed list only while a line is down.
func TestMapLabStatusConsoleCables(t *testing.T) {
	got := mapLabStatus(&labpb.Lab{Status: &labpb.LabStatus{ConsoleCables: []*labpb.ConsoleCableStatus{
		{SerialDevice: "pc1", SerialPort: "serial0", ConsoleDevice: "r1", ConsolePort: "console0", LineUp: true, Reason: "stale"},
		{SerialDevice: "pc1", SerialPort: "serial1", ConsoleDevice: "sw", ConsolePort: "console0", Reason: "PeerUnreachable"},
		{SerialDevice: "pc2", SerialPort: "serial0", ConsoleDevice: "fw", ConsolePort: "aux0", Reason: "dial tcp 10.0.0.7: refused"},
	}}})
	require.Equal(t, []exerciseModel.LabConsoleCable{
		{SerialDevice: "pc1", SerialPort: "serial0", ConsoleDevice: "r1", ConsolePort: "console0", LineUp: true},
		{SerialDevice: "pc1", SerialPort: "serial1", ConsoleDevice: "sw", ConsolePort: "console0", Reason: exerciseModel.ConsoleCablePeerUnreachable},
		{SerialDevice: "pc2", SerialPort: "serial0", ConsoleDevice: "fw", ConsolePort: "aux0", Reason: exerciseModel.ConsoleCableUnknown},
	}, got.ConsoleCables)
}

// Ports go to their device, cables to spec.consoleCables (serial end first whatever the order of the endpoints), never to
// spec.connections; a device with ports has a service block, so the launcher starts its port keeper.
func TestBuildLabSpec_ConsolePortsAndCables(t *testing.T) {
	encoded, _, err := BuildLabSpec(cabledLab())
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(encoded, &raw))
	require.Equal(t, []any{map[string]any{
		"serial":  map[string]any{"device": "pc1", "port": "serial0"},
		"console": map[string]any{"device": "r1", "port": "console0"},
	}}, raw["consoleCables"], "the shape of the lab spec (console design 14.1)")
	require.NotContains(t, raw, "connections")

	spec := decode(t, encoded)
	pc, r1 := spec.Devices[0], spec.Devices[1]
	require.Equal(t, []labSerialPort{{Name: "serial0"}, {Name: "serial1", Path: "/dev/ttyUSB1"}}, pc.SerialPorts)
	require.Equal(t, []labConsolePort{
		{Name: "console0", Command: []string{"/usr/bin/vtysh"}, User: "frr"},
		{Name: "aux0"},
		{Name: "console1", Native: &labNativeSource{Kind: "tcp", Port: 2001}},
	}, r1.ConsolePorts, "the default command is the laboratory's to fill")
	require.Equal(t, &labService{Command: []string{"/bin/sleep"}, Args: []string{"infinity"}, User: "0"}, pc.Service,
		"a device with ports always has a service and its user")
	require.Equal(t, &labService{Command: []string{"/start"}, User: "frr"}, r1.Service)

	devices := raw["devices"].([]any)
	require.Contains(t, devices[1].(map[string]any), "consolePorts")
	require.Contains(t, devices[0].(map[string]any), "serialPorts")
	require.Equal(t, map[string]any{"kind": "tcp", "port": float64(2001)}, devices[1].(map[string]any)["consolePorts"].([]any)[2].(map[string]any)["native"])
}

// A lab without ports sends none of the keys, so an older agent and every stored spec stay as they are.
func TestBuildLabSpec_NoConsoleKeysWithoutPorts(t *testing.T) {
	encoded, _, err := BuildLabSpec(nodeOf(""))
	require.NoError(t, err)
	for _, key := range []string{"consoleCables", "consolePorts", "serialPorts", "service"} {
		require.NotContains(t, string(encoded), key)
	}
}

// Each feature is pinned to the contract that brought it: a 3.0 agent keeps its access grants (a lab without consoles is
// unaffected) and is never sent console lines, even when its report would name the feature.
func TestContractPinsPerFeature(t *testing.T) {
	require.True(t, contractAccepts("3.0"), "grants need contract 3.0")
	require.True(t, contractAccepts("3.1"))
	require.False(t, contractAccepts("2.9"))
	old := consoleOffered(64)
	old.ContractVersion = "3.0"
	m := &Member{Enabled: true, Features: NewFeatureCell(old)}
	require.False(t, servesConsoleLines(m), "console lines need contract 3.1")
	require.ErrorIs(t, NewFleet(nil, nil, m).RequireConsoleLines(cabledLab()), exerciseModel.ErrConsoleUnavailable.Err())
}

// A refusal never stands on a stale cached report: the reports are read again once, for the fleet-wide check and for the
// placement of a new group.
func TestConsoleLinesRefreshAStaleReportBeforeRefusing(t *testing.T) {
	f := newFleetFixture(t)
	f.am.Features = NewFeatureCell(allOptions())
	f.bm.Features = NewFeatureCell(allOptions())
	f.a.features = &labpb.FeaturesResponse{
		ContractVersion: "3.1", Checks: &labpb.ChecksFeature{Enabled: true}, Access: &labpb.AccessFeature{Http: true, Terminal: true},
		Bootstrap: &labpb.BootstrapFeature{}, ConsoleCables: &labpb.ConsoleCablesFeature{Enabled: true, MaxCablesPerLab: 64},
	}
	require.NoError(t, f.fleet.DeployLab(context.Background(), "e-1-t-1", "c-1", infraModel.LabMeta{}, cabledLab()))
	require.NotNil(t, f.a.createLabs, "the agent switched on since its last report takes the lab")
}
