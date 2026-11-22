package labagent

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	labclient "github.com/cybericebox/laboratory/pkg/agent/client"
	labpb "github.com/cybericebox/laboratory/pkg/agent/protobuf"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	infraModel "github.com/cybericebox/backend/internal/model/infrastructure"
)

// The features of contract 2.8 are read as the agent reports them; a field the agent does not send (an older agent) stays unknown
// and means "served", never "off".
func TestFeaturesOfReadsTheFeatureSwitches(t *testing.T) {
	off, on := false, true
	t.Run("an older agent reports none of them", func(t *testing.T) {
		got := FeaturesOf(&labpb.FeaturesResponse{Vpn: &labpb.VPNFeature{ReverseConnections: true}})
		assert.Nil(t, got.VPN.Enabled)
		assert.Nil(t, got.Internet)
		assert.Nil(t, got.AllowedRegistries)
		assert.True(t, got.ServesVPN())
		assert.True(t, got.ServesInternet())
		assert.True(t, got.AllowsRegistry("ghcr.io"))
	})
	t.Run("the switches are off", func(t *testing.T) {
		got := FeaturesOf(&labpb.FeaturesResponse{
			Vpn: &labpb.VPNFeature{Enabled: &off}, Internet: &labpb.InternetFeature{Enabled: false},
			Access:            &labpb.AccessFeature{Http: true, Rdp: false, Vnc: false},
			AllowedRegistries: &labpb.AllowedRegistriesFeature{Registries: []string{"docker.io", "ghcr.io"}},
		})
		require.NotNil(t, got.VPN.Enabled)
		assert.False(t, got.ServesVPN())
		assert.False(t, got.ServesInternet())
		assert.False(t, got.ServesAccess("rdp"))
		assert.False(t, got.ServesAccess("vnc"))
		assert.True(t, got.AllowsRegistry("ghcr.io"))
		assert.False(t, got.AllowsRegistry("gcr.io"))
	})
	t.Run("the switches are on, any registry", func(t *testing.T) {
		got := FeaturesOf(&labpb.FeaturesResponse{
			Vpn: &labpb.VPNFeature{Enabled: &on}, Internet: &labpb.InternetFeature{Enabled: true},
			Access:            &labpb.AccessFeature{Http: true, Rdp: true, Vnc: true},
			AllowedRegistries: &labpb.AllowedRegistriesFeature{Any: true},
		})
		assert.True(t, got.ServesVPN())
		assert.True(t, got.ServesInternet())
		assert.True(t, got.ServesAccess("rdp"))
		assert.True(t, got.AllowsRegistry("anything.example.com:5000"))
	})
}

// A refused item is a planning error, not a wait: it is not retried even when the agent calls it retryable, and it reaches the
// admin as an unavailable option of the laboratory.
func TestTheUnavailableReasonsArePlanningErrorsNotRetries(t *testing.T) {
	for reason, option := range map[string]string{
		infraModel.ReasonVPNUnavailable:      "vpn",
		infraModel.ReasonInternetUnavailable: "internet",
		infraModel.ReasonAccessUnavailable:   "access",
		infraModel.ReasonChecksUnavailable:   "checks",
	} {
		t.Run(reason, func(t *testing.T) {
			f := &fakeAgent{groups: groupWith(true, true, false), itemState: labpb.ItemState_ITEM_STATE_FAILED, itemReason: reason, retryable: true, itemError: "refused"}
			err := newClient(f).DeployLab(context.Background(), "event-team", "lab", infraModel.LabMeta{}, vpnTopology())
			require.ErrorIs(t, err, exerciseModel.ErrLabOptionUnavailable.Err())
			assert.True(t, infraModel.IsPlanningError(err))
			_, terminating := infraModel.AsTerminating(err)
			assert.False(t, terminating)
			assert.Contains(t, err.Error(), option)
		})
	}
}

// The client and the access refusals of the group calls are the same planning errors.
func TestTheUnavailableReasonsOfAClientAreNotRetried(t *testing.T) {
	f := &fakeAgent{groups: groupWith(true, true, false), itemState: labpb.ItemState_ITEM_STATE_FAILED, itemReason: infraModel.ReasonVPNUnavailable, retryable: true, itemError: "refused"}
	_, err := newClient(f).EnsureLabClient(context.Background(), "event-team", "p-user")
	require.ErrorIs(t, err, exerciseModel.ErrLabOptionUnavailable.Err())
	assert.True(t, infraModel.IsPlanningError(err))
	_, terminating := infraModel.AsTerminating(err)
	assert.False(t, terminating)
}

func registryNotAllowed(message string) error {
	st := status.New(codes.PermissionDenied, message)
	st, _ = st.WithDetails(&errdetails.ErrorInfo{Domain: labclient.ErrorDomain, Reason: labclient.ReasonRegistryNotAllowed})
	return st.Err()
}

// REGISTRY_NOT_ALLOWED of the agent (PermissionDenied) is a planning error that keeps the agent's text for the admin.
func TestRegistryNotAllowedIsAPlanningError(t *testing.T) {
	f := &fakeAgent{groups: groupWith(true, true, false), callErr: registryNotAllowed(`variant 1, device "web": image gcr.io/x/y is from registry gcr.io`)}
	err := newClient(f).DeployLab(context.Background(), "event-team", "lab", infraModel.LabMeta{}, vpnTopology())
	require.ErrorIs(t, err, exerciseModel.ErrLabOptionUnavailable.Err())
	assert.True(t, infraModel.IsPlanningError(err))
	assert.Contains(t, err.Error(), "gcr.io")
	_, terminating := infraModel.AsTerminating(err)
	assert.False(t, terminating)

	other := agentErr("deploy", status.Error(codes.PermissionDenied, "forbidden"))
	assert.False(t, infraModel.IsPlanningError(other), "any other refusal stays as it was")
	assert.False(t, infraModel.IsPlanningError(agentErr("deploy", errors.New("boom"))))
}

func offFeatures() *infraModel.AgentFeatures {
	off := false
	return &infraModel.AgentFeatures{
		VPN: &infraModel.VPNFeature{Enabled: &off}, Internet: &infraModel.InternetFeature{Enabled: false},
		Access:            &infraModel.AccessFeature{HTTP: true},
		AllowedRegistries: &infraModel.AllowedRegistriesFeature{Registries: []string{"docker.io"}},
	}
}

// Where the daemon already refuses a lab for an option the laboratory lacks (the fleet's requireLabOptions), the switches join:
// nothing is sent to the agent.
func TestFleetRefusesALabEarlyForWhatTheClusterDoesNotServe(t *testing.T) {
	ctx := context.Background()
	for name, topo := range map[string]exerciseModel.Topology{
		"a VPN gateway":      vpnTopology(vpnGateway("attack", false)),
		"an Internet gate":   vpnTopology(exerciseModel.Device{Name: "net", Type: exerciseModel.DeviceTypeInternetGateway}),
		"an RDP access":      {Devices: []exerciseModel.Device{{Name: "win", Type: exerciseModel.DeviceTypeNode, Image: "docker.io/win", Access: []exerciseModel.Access{{Kind: exerciseModel.AccessRDP}}}}},
		"a VNC access":       {Devices: []exerciseModel.Device{{Name: "desk", Type: exerciseModel.DeviceTypeNode, Image: "docker.io/desk", Access: []exerciseModel.Access{{Kind: exerciseModel.AccessVNC}}}}},
		"another registry":   {Devices: []exerciseModel.Device{{Name: "web", Type: exerciseModel.DeviceTypeNode, Image: "gcr.io/x/web"}}},
		"a private registry": {Devices: []exerciseModel.Device{{Name: "web", Type: exerciseModel.DeviceTypeNode, Image: "registry.example.com:5000/web"}}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFleetFixture(t)
			f.am.Features = NewFeatureCell(offFeatures())
			err := f.fleet.DeployLab(ctx, "e-1-t-1", "c-1", infraModel.LabMeta{}, topo)
			require.ErrorIs(t, err, exerciseModel.ErrLabOptionUnavailable.Err())
			assert.True(t, infraModel.IsPlanningError(err))
			assert.Nil(t, f.a.createLabs, "the agent is not asked")
		})
	}
	t.Run("a lab the cluster serves goes through", func(t *testing.T) {
		f := newFleetFixture(t)
		f.am.Features = NewFeatureCell(offFeatures())
		topo := exerciseModel.Topology{Devices: []exerciseModel.Device{{Name: "web", Type: exerciseModel.DeviceTypeNode, Image: "nginx"}}}
		require.NoError(t, f.fleet.DeployLab(ctx, "e-1-t-1", "c-1", infraModel.LabMeta{}, topo))
		assert.NotNil(t, f.a.createLabs)
	})
}

// A group is made with its VPN on, and with the internet gateway when the plan has internet labs: a laboratory without them is
// not asked.
func TestFleetRefusesAGroupEarlyForServersTheClusterDoesNotRun(t *testing.T) {
	ctx := context.Background()
	t.Run("no VPN", func(t *testing.T) {
		f := newFleetFixture(t)
		f.am.Features = NewFeatureCell(offFeatures())
		err := f.fleet.EnsureVPNGroup(ctx, "e-1-t-1")
		require.ErrorIs(t, err, exerciseModel.ErrLabOptionUnavailable.Err())
		assert.True(t, infraModel.IsPlanningError(err))
		assert.Nil(t, f.a.createGroup)
	})
	t.Run("the VPN runs, the internet gateway does not, and an internet lab is planned", func(t *testing.T) {
		features := offFeatures()
		on := true
		features.VPN.Enabled = &on
		plan := infraModel.GroupPlan{InternetLabs: 2}
		for name, c := range map[string]struct {
			plan    infraModel.GroupPlan
			refused bool
		}{"with internet labs": {plan, true}, "without": {infraModel.GroupPlan{}, false}} {
			t.Run(name, func(t *testing.T) {
				f := newFleetFixture(t)
				f.am.Features = NewFeatureCell(features)
				err := f.fleet.EnsureVPNGroup(infraModel.WithPlacementNeed(ctx, infraModel.PlacementNeed{Plan: c.plan}), "e-1-t-1")
				if c.refused {
					require.ErrorIs(t, err, exerciseModel.ErrLabOptionUnavailable.Err())
					assert.Contains(t, err.Error(), "internet")
					assert.Nil(t, f.a.createGroup)
					return
				}
				require.NoError(t, err)
				assert.NotNil(t, f.a.createGroup)
			})
		}
	})
	t.Run("an agent that has not reported is not refused here", func(t *testing.T) {
		f := newFleetFixture(t)
		require.NoError(t, f.fleet.EnsureVPNGroup(ctx, "e-1-t-1"))
	})
}
