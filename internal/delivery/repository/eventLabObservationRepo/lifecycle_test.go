package eventLabObservationRepo

import (
	"context"
	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	labpb "github.com/cybericebox/laboratory/pkg/agent/protobuf"
	"github.com/gofrs/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"testing"
	"time"
)

// A group whose VPN is switched off has no VPN server to register or boot (contract 2.7): it is ready without them, and its release
// fence needs no VPN boot. A group with the VPN on keeps both requirements.
func TestGroupProjectionWithTheVPNOffNeedsNoVPNBoot(t *testing.T) {
	at := time.Now().UTC()
	team := uuid.Must(uuid.NewV7())
	event := uuid.Must(uuid.NewV7())
	operation := uuid.Must(uuid.NewV7())
	for _, vpn := range []bool{false, true} {
		status := &labpb.LabGroupStatus{Phase: "Ready", Lifecycle: &labpb.LabLifecycleStatus{LabUid: "group-uid", OperationId: operation.String(), LifecycleRevision: 1, ObservedGeneration: 3, DesiredState: "Running", ObservedState: "Running", Reason: "Normal"}}
		raw, err := protojson.Marshal(&labpb.MonitoringUpdate{Groups: []*labpb.LabGroup{{Name: "group", Uid: "group-uid", Generation: 3, VpnEnabled: vpn, Status: status}}})
		if err != nil {
			t.Fatal(err)
		}
		q := &currentQueries{rows: map[uuid.UUID]postgres.LabMonitoringCurrent{team: {Payload: raw, ObservedAt: at}}}
		got, err := New(q).Group(context.Background(), event, team, "group")
		if err != nil {
			t.Fatal(err)
		}
		if got.Ready == vpn {
			t.Fatalf("vpn enabled=%v: ready=%v, a group with the VPN off is ready without a VPN server and one with it on is not", vpn, got.Ready)
		}
	}
}

// The release of a stopped group is credited with a fence, which names a VPN boot only when there is a VPN server.
func TestGroupReleaseFenceNeedsNoVPNBootWithTheVPNOff(t *testing.T) {
	at := time.Now().UTC()
	team, event, operation := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, vpn := range []bool{false, true} {
		status := &labpb.LabGroupStatus{Phase: "Suspended", Lifecycle: &labpb.LabLifecycleStatus{LabUid: "group-uid", OperationId: operation.String(), LifecycleRevision: 1, ObservedGeneration: 3, DesiredState: "Stopped", ObservedState: "Stopped",
			AccessFenced: true, AccessFencedUnixMs: at.UnixMilli()},
			Resources: &labpb.ResourceAllocation{OperationId: operation.String(), LifecycleRevision: 1, RuntimeState: "Released", ObservedUnixMs: at.UnixMilli(), ReleasedUnixMs: at.UnixMilli()}}
		raw, err := protojson.Marshal(&labpb.MonitoringUpdate{Groups: []*labpb.LabGroup{{Name: "group", Uid: "group-uid", Generation: 3, VpnEnabled: vpn, Status: status}}})
		if err != nil {
			t.Fatal(err)
		}
		q := &currentQueries{rows: map[uuid.UUID]postgres.LabMonitoringCurrent{team: {Payload: raw, ObservedAt: at}}}
		got, err := New(q).Group(context.Background(), event, team, "group")
		if err != nil {
			t.Fatal(err)
		}
		if released := got.Allocation.RuntimeState == "Released"; released == vpn {
			t.Fatalf("vpn enabled=%v: release credited=%v, a fence without a boot id counts only for a group with the VPN off: %+v", vpn, released, got.Allocation)
		}
	}
}

func TestGroupProjectionNeedsCurrentNetworkAndPhysicalReleaseProof(t *testing.T) {
	at := time.Now()
	team := uuid.Must(uuid.NewV7())
	event := uuid.Must(uuid.NewV7())
	operation := uuid.Must(uuid.NewV7())
	for _, mode := range []string{"missing current boot", "normal running", "missing release fence"} {
		t.Run(mode, func(t *testing.T) {
			status := &labpb.LabGroupStatus{Phase: "Ready", VpnRegistered: true, CurrentVpnBootAvailable: true, CurrentVpnBootId: "current-boot", CurrentVpnBootObservedUnixMs: at.UnixMilli(), Lifecycle: &labpb.LabLifecycleStatus{LabUid: "group-uid", OperationId: operation.String(), LifecycleRevision: 1, ObservedGeneration: 3, DesiredState: "Running", ObservedState: "Running", Reason: "Normal"}}
			if mode == "missing current boot" {
				status.CurrentVpnBootAvailable = false
			}
			if mode == "missing release fence" {
				status.Lifecycle.DesiredState = "Stopped"
				status.Lifecycle.ObservedState = "Stopped"
				status.Resources = &labpb.ResourceAllocation{OperationId: operation.String(), LifecycleRevision: 1, RuntimeState: "Released", ObservedUnixMs: at.UnixMilli(), ReleasedUnixMs: at.UnixMilli()}
			}
			raw, err := protojson.Marshal(&labpb.MonitoringUpdate{Groups: []*labpb.LabGroup{{Name: "group", Uid: "group-uid", Generation: 3, VpnEnabled: true, Status: status}}})
			if err != nil {
				t.Fatal(err)
			}
			q := &currentQueries{rows: map[uuid.UUID]postgres.LabMonitoringCurrent{team: {Payload: raw, ObservedAt: at}}}
			got, err := New(q).Group(context.Background(), event, team, "group")
			if err != nil {
				t.Fatal(err)
			}
			if got.Ready != (mode == "normal running") || got.FailureCode != "" || got.FailureMessage != "" {
				t.Fatalf("readiness/failure %+v", got)
			}
			if mode == "missing release fence" && got.Allocation.RuntimeState == "Released" {
				t.Fatalf("unfenced group release credited %+v", got)
			}
		})
	}
}
