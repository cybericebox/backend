package labagent

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"

	labpb "github.com/cybericebox/laboratory/pkg/agent/protobuf"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	infraModel "github.com/cybericebox/backend/internal/model/infrastructure"
)

// kernelLogLab is cabledLab with its router a microVM that asks for the kernel log.
func kernelLogLab() exerciseModel.Topology {
	topo := cabledLab()
	topo.Devices[1].Profile = exerciseModel.NodeProfileMicroVM
	topo.Devices[1].KernelLog = true
	return topo
}

func kernelLogOffered() *infraModel.AgentFeatures {
	f := consoleOffered(64)
	f.ContractVersion = "3.2"
	f.ConsoleCables.KernelLog = true
	return f
}

// withKernelLogField is a console feature whose kernel_log (field 4, contract 3.2) arrives as the agent sends it on the wire:
// an SDK older than the field keeps it among the unknown fields. When the laboratory pin moves to an SDK that declares field 4,
// replace this helper with the field itself (ConsoleCablesFeature{..., KernelLog: true}) and kernelLogOf with GetKernelLog().
func withKernelLogField(c *labpb.ConsoleCablesFeature, on bool) *labpb.ConsoleCablesFeature {
	v := uint64(0)
	if on {
		v = 1
	}
	b := protowire.AppendTag(nil, 4, protowire.VarintType)
	c.ProtoReflect().SetUnknown(protowire.AppendVarint(b, v))
	return c
}

func TestFeaturesOfReadsTheKernelLogOfContract32(t *testing.T) {
	on := FeaturesOf(&labpb.FeaturesResponse{ConsoleCables: withKernelLogField(&labpb.ConsoleCablesFeature{Enabled: true, MaxCablesPerLab: 64}, true)})
	require.True(t, on.ConsoleCables.KernelLog)
	require.True(t, on.ServesKernelLog())
	off := FeaturesOf(&labpb.FeaturesResponse{ConsoleCables: withKernelLogField(&labpb.ConsoleCablesFeature{Enabled: true}, false)})
	require.False(t, off.ConsoleCables.KernelLog)
	old := FeaturesOf(&labpb.FeaturesResponse{ConsoleCables: &labpb.ConsoleCablesFeature{Enabled: true}})
	require.False(t, old.ConsoleCables.KernelLog, "a 3.1 agent reports no kernel log")
}

// The kernel log needs an agent of contract 3.2 that reports console_cables.kernel_log; console lines alone are not enough.
func TestKernelLogOfTheFleet(t *testing.T) {
	topo := kernelLogLab()
	lines := &Member{Enabled: true, Features: NewFeatureCell(consoleOffered(64))}
	f := NewFleet(nil, nil, lines)
	require.True(t, f.ConsoleCablesAvailable())
	require.False(t, f.KernelLogAvailable())
	require.ErrorIs(t, f.RequireConsoleLines(topo), exerciseModel.ErrKernelLogUnavailable.Err(), "named rather than the absence of console lines")
	require.NoError(t, f.RequireConsoleLines(cabledLab()), "a lab without the kernel log still runs there")

	flagOn31 := consoleOffered(64)
	flagOn31.ConsoleCables.KernelLog = true
	old := &Member{Enabled: true, Features: NewFeatureCell(flagOn31)}
	require.False(t, NewFleet(nil, nil, old).KernelLogAvailable(), "the kernel log needs contract 3.2")
	require.ErrorIs(t, NewFleet(nil, nil, old).RequireConsoleLines(topo), exerciseModel.ErrKernelLogUnavailable.Err())

	off32 := kernelLogOffered()
	off32.ConsoleCables.KernelLog = false
	require.False(t, NewFleet(nil, nil, &Member{Enabled: true, Features: NewFeatureCell(off32)}).KernelLogAvailable(), "3.2 with the feature off")

	on := &Member{Enabled: true, Features: NewFeatureCell(kernelLogOffered())}
	f = NewFleet(nil, nil, lines, on)
	require.True(t, f.KernelLogAvailable())
	require.NoError(t, f.RequireConsoleLines(topo))
	require.False(t, NewFleet(nil, nil, &Member{Enabled: false, Features: NewFeatureCell(kernelLogOffered())}).KernelLogAvailable(), "a disabled agent is not used")

	require.ErrorIs(t, NewFleet(nil, nil, lines).DeployLab(t.Context(), "g", "l", infraModel.LabMeta{}, topo), exerciseModel.ErrKernelLogUnavailable.Err())
	require.True(t, infraModel.IsPlanningError(exerciseModel.ErrKernelLogUnavailable.Err()), "never retried as it is")
}

// A new group whose lab asks for the kernel log goes to the agent that offers it, not to one that serves console lines only.
func TestANewGroupWithTheKernelLogGoesToAnAgentThatOffersIt(t *testing.T) {
	f := newFleetFixture(t)
	f.am.Features = NewFeatureCell(consoleOffered(64))
	f.bm.Features = NewFeatureCell(kernelLogOffered())
	require.NoError(t, f.fleet.DeployLab(t.Context(), "e-1-t-9", "c-1", infraModel.LabMeta{}, kernelLogLab()))
	require.Nil(t, f.a.createLabs, "the 3.1 agent gets nothing")
	require.NotNil(t, f.b.createLabs)
	require.True(t, labNeed(f.fleet.Policy(), kernelLogLab()).KernelLog)
	require.True(t, withLab(infraModel.PlacementNeed{}, infraModel.PlacementNeed{KernelLog: true}).KernelLog)
}

// A cached report that serves console lines but says kernel_log=false is read again before a refusal stands: the feature can
// be switched on after the agent reported.
func TestTheKernelLogRefreshesAStaleReportBeforeRefusing(t *testing.T) {
	f := newFleetFixture(t)
	stale := kernelLogOffered()
	stale.ConsoleCables.KernelLog = false
	f.am.Features = NewFeatureCell(stale)
	f.bm.Features = NewFeatureCell(allOptions())
	f.a.features = &labpb.FeaturesResponse{
		ContractVersion: "3.2", Checks: &labpb.ChecksFeature{Enabled: true}, Access: &labpb.AccessFeature{Http: true, Terminal: true},
		Bootstrap: &labpb.BootstrapFeature{}, ConsoleCables: withKernelLogField(&labpb.ConsoleCablesFeature{Enabled: true, MaxCablesPerLab: 64}, true),
	}
	require.NoError(t, f.fleet.DeployLab(t.Context(), "e-1-t-1", "c-1", infraModel.LabMeta{}, kernelLogLab()))
	require.NotNil(t, f.a.createLabs, "the agent switched on since its last report takes the lab")
}

func TestDeployLabSendsTheKernelLogToAnAgentThatOffersIt(t *testing.T) {
	f := newFleetFixture(t)
	f.am.Features = NewFeatureCell(kernelLogOffered())
	require.NoError(t, f.fleet.DeployLab(t.Context(), "e-1-t-1", "c-1", infraModel.LabMeta{}, kernelLogLab()))
	spec := decode(t, f.a.createLabs.GetVariants()[0].GetSpecJson())
	require.True(t, spec.Devices[1].ConsolePorts[0].KernelLog)
}

// The flag goes on the first console port that is not native, in declared order, and on no other port; without the flag the
// key is never written, so an older agent and every stored spec stay as they are.
func TestBuildLabSpec_KernelLogOnTheFirstPortThatIsNotNative(t *testing.T) {
	topo := kernelLogLab()
	r1 := &topo.Devices[1]
	r1.ConsolePorts = append([]exerciseModel.ConsolePort{{Name: "aux1", Native: &exerciseModel.NativeSource{Kind: exerciseModel.NativeStdio}}}, r1.ConsolePorts...)
	encoded, _, err := BuildLabSpec(topo)
	require.NoError(t, err)
	ports := decode(t, encoded).Devices[1].ConsolePorts
	require.Len(t, ports, 4)
	require.Equal(t, []bool{false, true, false, false}, []bool{ports[0].KernelLog, ports[1].KernelLog, ports[2].KernelLog, ports[3].KernelLog})
	require.Equal(t, "console0", ports[1].Name)

	plain, _, err := BuildLabSpec(cabledLab())
	require.NoError(t, err)
	require.NotContains(t, string(plain), "kernelLog")
}
