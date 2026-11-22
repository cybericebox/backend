package labagent

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	labpb "github.com/cybericebox/laboratory/pkg/agent/protobuf"

	eventLabModel "github.com/cybericebox/backend/internal/model/eventLab"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	infraModel "github.com/cybericebox/backend/internal/model/infrastructure"
)

func internetGateway() exerciseModel.Device {
	return exerciseModel.Device{ID: uuid.Must(uuid.NewV7()), Name: "out", Type: exerciseModel.DeviceTypeInternetGateway}
}

func groupWith(vpn, internet, suspended bool) []*labpb.LabGroup {
	return []*labpb.LabGroup{{Name: "event-team", VpnEnabled: vpn, InternetEnabled: internet,
		Status: &labpb.LabGroupStatus{Namespace: "ns-team", Suspended: suspended}}}
}

// A group is always made with both switches sent: the VPN on (it has clients), the internet gateway on only when the plan has labs
// that use the internet. An item that sent only one would leave the other off, and one that sent neither the old shape.
func TestANewGroupAlwaysCarriesBothSwitches(t *testing.T) {
	for name, tc := range map[string]struct {
		plan     infraModel.GroupPlan
		internet bool
	}{
		"no internet labs":  {infraModel.GroupPlan{MaxUsers: 3}, false},
		"with internet lab": {infraModel.GroupPlan{MaxUsers: 3, InternetLabs: 2}, true},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeAgent{groups: readyGroup()}
			ctx := infraModel.WithPlacementNeed(context.Background(), infraModel.PlacementNeed{Plan: tc.plan})
			require.NoError(t, newClient(f).EnsureVPNGroup(ctx, "event-team"))
			item := f.createGroup.GetItems()[0]
			require.NotNil(t, item.VpnEnabled, "vpn_enabled is always sent")
			require.NotNil(t, item.InternetEnabled, "internet_enabled is always sent")
			assert.True(t, item.GetVpnEnabled())
			assert.Equal(t, tc.internet, item.GetInternetEnabled())
			assert.False(t, item.GetVpnDisabled(), "the old flag is not used")
		})
	}
}

// A lab with an Internet connector makes the group's internet gateway on at creation, even when the plan did not count it.
func TestADeployedLabWithAnInternetConnectorCreatesTheGroupWithInternetOn(t *testing.T) {
	f := &fakeAgent{groups: groupWith(true, true, false)}
	require.NoError(t, newClient(f).DeployLab(context.Background(), "event-team", "lab", infraModel.LabMeta{}, vpnTopology(vpnGateway("vpn", false), internetGateway())))
	assert.True(t, f.createGroup.GetItems()[0].GetInternetEnabled())
	assert.True(t, f.createGroup.GetItems()[0].GetVpnEnabled())
	assert.Nil(t, f.updateGroup, "a group that has the servers on needs no switching")
	f2 := &fakeAgent{groups: groupWith(true, false, false)}
	require.NoError(t, newClient(f2).DeployLab(context.Background(), "event-team", "lab", infraModel.LabMeta{}, vpnTopology(vpnGateway("vpn", false))))
	assert.False(t, f2.createGroup.GetItems()[0].GetInternetEnabled(), "no internet connector: the gateway stays off")
}

// A server that a lab needs is switched on before the lab is made: the agent would refuse the lab otherwise.
func TestAnExistingGroupHasTheServersTurnedOnBeforeTheLab(t *testing.T) {
	f := &fakeAgent{groups: groupWith(false, false, false)}
	require.NoError(t, newClient(f).DeployLab(context.Background(), "event-team", "lab", infraModel.LabMeta{}, vpnTopology(vpnGateway("vpn", false), internetGateway())))
	require.NotNil(t, f.updateGroup, "the switches are turned on")
	changes := f.updateGroup.GetItems()[0].GetChanges()
	assert.True(t, changes.GetVpnEnabled())
	assert.True(t, changes.GetInternetEnabled())
	assert.NotNil(t, f.createLabs, "and then the lab is made")
}

func TestOnlyTheServerTheLabNeedsIsTurnedOn(t *testing.T) {
	f := &fakeAgent{groups: groupWith(true, false, false)}
	require.NoError(t, newClient(f).DeployLab(context.Background(), "event-team", "lab", infraModel.LabMeta{}, vpnTopology(internetGateway())))
	changes := f.updateGroup.GetItems()[0].GetChanges()
	assert.Nil(t, changes.VpnEnabled, "the VPN is on already")
	assert.True(t, changes.GetInternetEnabled())

	// a lab without connectors needs no server
	f2 := &fakeAgent{groups: groupWith(false, false, false)}
	require.NoError(t, newClient(f2).DeployLab(context.Background(), "event-team", "lab", infraModel.LabMeta{}, vpnTopology()))
	assert.Nil(t, f2.updateGroup)
}

// A group that is suspended on purpose (its access is closed) keeps its VPN off: the lab is refused with ErrGroupVPNOff and made
// when the access sync turns the VPN on.
func TestASuspendedGroupsVPNIsNotTurnedOnByALab(t *testing.T) {
	f := &fakeAgent{groups: groupWith(false, true, true)}
	require.NoError(t, newClient(f).DeployLab(context.Background(), "event-team", "lab", infraModel.LabMeta{}, vpnTopology(vpnGateway("vpn", false))))
	assert.Nil(t, f.updateGroup)
}

// A VPN client needs the VPN server.
func TestAClientTurnsTheVPNOnFirst(t *testing.T) {
	f := &fakeAgent{groups: groupWith(false, false, false), clientCfg: "cfg"}
	_, err := newClient(f).EnsureLabClient(context.Background(), "event-team", "p-user")
	require.NoError(t, err)
	assert.True(t, f.updateGroup.GetItems()[0].GetChanges().GetVpnEnabled())
	assert.NotNil(t, f.createCl)
	f2 := &fakeAgent{groups: groupWith(true, false, false), clientCfg: "cfg"}
	_, err = newClient(f2).EnsureLabClient(context.Background(), "event-team", "p-user")
	require.NoError(t, err)
	assert.Nil(t, f2.updateGroup, "the VPN is on already")
}

// GROUP_VPN_OFF, GROUP_INTERNET_OFF and GROUP_SERVER_IN_USE are planning errors: they come back as their own errors even when
// the agent calls them retryable, so nothing treats them as "wait and try again".
func TestTheSwitchRefusalsArePlanningErrorsNotRetries(t *testing.T) {
	for reason, want := range map[string]error{
		infraModel.ReasonGroupVPNOff:      infraModel.ErrGroupVPNOff,
		infraModel.ReasonGroupInternetOff: infraModel.ErrGroupInternetOff,
		infraModel.ReasonGroupServerInUse: infraModel.ErrGroupServerInUse,
	} {
		t.Run(reason, func(t *testing.T) {
			f := &fakeAgent{groups: groupWith(true, true, false), itemState: labpb.ItemState_ITEM_STATE_FAILED, itemReason: reason, retryable: true, itemError: "refused"}
			c := newClient(f)
			for name, err := range map[string]error{
				"update group": c.SetLabGroupVPNDisabled(context.Background(), "event-team", true),
				"create lab":   c.DeployLab(context.Background(), "event-team", "lab", infraModel.LabMeta{}, vpnTopology()),
			} {
				if name == "create lab" && reason == infraModel.ReasonGroupServerInUse {
					continue
				}
				require.ErrorIs(t, err, want, name)
				assert.True(t, infraModel.IsPlanningError(err), name)
				_, terminating := infraModel.AsTerminating(err)
				assert.False(t, terminating, name+": not a wait for deletion")
			}
			_, err := c.EnsureLabClient(context.Background(), "event-team", "p-user")
			require.ErrorIs(t, err, want)
			_, terminating := infraModel.AsTerminating(err)
			assert.False(t, terminating)
		})
	}
	assert.False(t, infraModel.IsPlanningError(nil))
	assert.False(t, infraModel.IsPlanningError(infraModel.ErrReservationFull))
}

// ── readiness and fences ───────────────────────────────────────────────────

func groupForReadiness(vpnEnabled bool, registered, bootAvailable bool) *labpb.LabGroup {
	at := nowMillis()
	return &labpb.LabGroup{Name: "g", Uid: "uid", Generation: 3, VpnEnabled: vpnEnabled,
		VpnSize: &labpb.PodSize{CpuMillicores: 10, MemoryBytes: 1}, GatewaySize: &labpb.PodSize{CpuMillicores: 10, MemoryBytes: 1},
		Status: &labpb.LabGroupStatus{Phase: "Ready", Namespace: "ns", VpnRegistered: registered, CurrentVpnBootAvailable: bootAvailable,
			CurrentVpnBootId: map[bool]string{true: "boot", false: ""}[bootAvailable], CurrentVpnBootObservedUnixMs: at}}
}

// With the VPN switched off a group is ready without a VPN server: no registration, no boot (contract 2.7). With it on, both stay
// required, as before.
func TestAGroupWithTheVPNOffIsReadyWithoutAVPNServer(t *testing.T) {
	now := nowTime()
	off := groupLifecycleObservation("g", groupForReadiness(false, false, false), now)
	assert.True(t, off.InitialReady && off.Ready, "VPN off: ready without registration or boot")
	on := groupLifecycleObservation("g", groupForReadiness(true, false, false), now)
	assert.False(t, on.InitialReady || on.Ready, "VPN on, not registered: not ready")
	registered := groupLifecycleObservation("g", groupForReadiness(true, true, true), now)
	assert.True(t, registered.InitialReady && registered.Ready)
	noBoot := groupLifecycleObservation("g", groupForReadiness(true, true, false), now)
	assert.False(t, noBoot.Ready, "VPN on, no current boot: not ready")
}

func TestAnUnreadyGroupIsNotReadyWhateverTheVPNSays(t *testing.T) {
	g := groupForReadiness(false, false, false)
	g.Status.Phase = "Provisioning"
	assert.False(t, groupLifecycleObservation("g", g, nowTime()).Ready)
	g = groupForReadiness(false, false, false)
	g.Status.Suspended = true
	assert.False(t, groupLifecycleObservation("g", g, nowTime()).Ready, "a suspended group is not ready")
}

func labWithFence(spec string, bootID string) *labpb.Lab {
	return &labpb.Lab{Name: "lab", SpecJson: []byte(spec), Uid: "lab-uid", Generation: 2, Status: &labpb.LabStatus{Lifecycle: &labpb.LabLifecycleStatus{
		LabUid: "lab-uid", ObservedGeneration: 2, OperationId: uuid.Must(uuid.NewV7()).String(), LifecycleRevision: 1, DesiredState: "Stopped", ObservedState: "Stopped",
		AccessFenced: true, AccessFencedUnixMs: 1000, AccessFenceVpnBootId: bootID}}}
}

// A lab without a VPN connector has no VPN boot to name: the agent reports it fenced without one and that counts. A lab with a VPN
// connector (the hidden control gateway included) still needs the boot id as evidence.
func TestTheFenceOfALabWithoutAVPNConnectorNeedsNoBootID(t *testing.T) {
	ref := refOf("g", "lab")
	cases := map[string]struct {
		spec, boot string
		fenced     bool
	}{
		"no gateways":             {`{"devices":[]}`, "", true},
		"internet gateway only":   {`{"gateways":[{"name":"out","kind":"internet"}]}`, "", true},
		"vpn connector no boot":   {`{"gateways":[{"name":"vpn","kind":"vpn"}]}`, "", false},
		"ctl connector no boot":   {`{"gateways":[{"name":"ctl","kind":"vpn"}]}`, "", false},
		"vpn connector with boot": {`{"gateways":[{"name":"vpn","kind":"vpn"}]}`, "boot-1", true},
		"unreadable spec":         {`not json`, "", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			o := lifecycleObservation(ref, labWithFence(tc.spec, tc.boot))
			assert.Equal(t, tc.fenced, o.AccessFenced)
		})
	}
}

// A group with the VPN off has no VPN boot to name either.
func TestTheFenceOfAGroupWithTheVPNOffNeedsNoBootID(t *testing.T) {
	for _, vpn := range []bool{false, true} {
		g := groupForReadiness(vpn, true, true)
		op := uuid.Must(uuid.NewV7()).String()
		g.Lifecycle = &labpb.GroupLifecycleSpec{DesiredState: "Running", OperationId: op, Revision: 1}
		g.Status.Lifecycle = &labpb.LabLifecycleStatus{LabUid: "uid", ObservedGeneration: 3, OperationId: op, LifecycleRevision: 1, DesiredState: "Running", ObservedState: "Running", AccessFenced: true, AccessFencedUnixMs: 5}
		o := groupLifecycleObservation("g", g, nowTime())
		assert.Equal(t, !vpn, o.AccessFenced, "vpn enabled=%v", vpn)
	}
}

// ── sizing ──────────────────────────────────────────────────────────────────

// A group with no internet lab runs no gateway pod: its reservation asks for none. The VPN is sized as before.
func TestAPlanWithoutInternetLabsReservesNoGatewayPod(t *testing.T) {
	l := serviceLimits(320 << 20)
	none := l.SizesFor(infraModel.GroupPlan{MaxUsers: 2})
	assert.Zero(t, none.Gateway.CPUMillicores)
	assert.Zero(t, none.Gateway.MemoryBytes)
	assert.NotZero(t, none.VPN.CPUMillicores)
	with := l.SizesFor(infraModel.GroupPlan{MaxUsers: 2, InternetLabs: 1})
	assert.NotZero(t, with.Gateway.CPUMillicores)
	assert.Equal(t, none.VPN, with.VPN)
}

func nowTime() time.Time { return time.Now().UTC() }
func nowMillis() int64   { return nowTime().UnixMilli() }
func refOf(group, lab string) eventLabModel.Ref {
	return eventLabModel.Ref{Group: group, Lab: lab}
}
