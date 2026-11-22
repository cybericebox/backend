package exercise_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"go.uber.org/mock/gomock"

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	postgresMocks "github.com/cybericebox/backend/internal/delivery/repository/postgres/mocks"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	"github.com/cybericebox/backend/internal/useCase/exercise"
)

// toolsFixture is a variant whose lab has a VPN gateway with DHCP (where the tools devices get their address) and a web server.
func toolsFixture(t *testing.T) (*postgresMocks.MockQuerier, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	q := postgresMocks.NewMockQuerier(gomock.NewController(t))
	q.EXPECT().ListOwnedExerciseTestDeploys(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	variantID, versionID, ownerID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	vpn := exerciseModel.Device{ID: uuid.Must(uuid.NewV7()), Name: "vpn", Type: exerciseModel.DeviceTypeVPNGateway, DHCP: true, DHCPRanges: []exerciseModel.DHCPRange{{Start: 100, End: 200}}}
	web := exerciseModel.Device{ID: uuid.Must(uuid.NewV7()), Name: "web", Type: exerciseModel.DeviceTypeNode, Profile: exerciseModel.NodeProfileContainer, Image: "nginx",
		Interfaces: []exerciseModel.Interface{{Name: "eth0", IP: exerciseModel.IPConfig{Type: exerciseModel.IPConfigTypeDHCPPreset}}}}
	topo := exerciseModel.Topology{Devices: []exerciseModel.Device{vpn, web}, Connections: []exerciseModel.Connection{{Endpoints: []exerciseModel.Endpoint{
		{Kind: exerciseModel.EndpointDevice, DeviceID: vpn.ID, Interface: exerciseModel.GatewayPort}, {Kind: exerciseModel.EndpointDevice, DeviceID: web.ID, Interface: "eth0"}}}}}
	variants, err := json.Marshal([]exerciseModel.Variant{{ID: variantID, Topology: topo}})
	if err != nil {
		t.Fatal(err)
	}
	q.EXPECT().GetExerciseVersionByID(gomock.Any(), versionID).Return(postgres.ExerciseVersion{ID: versionID, Variants: variants}, nil).AnyTimes()
	q.EXPECT().CreateExerciseTestDeploy(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, p postgres.CreateExerciseTestDeployParams) (postgres.ExerciseTestDeployment, error) {
		return postgres.ExerciseTestDeployment{ID: p.ID, EventID: p.EventID, GroupName: p.GroupName, CreatedBy: p.CreatedBy, ExpiresAt: p.ExpiresAt}, nil
	}).AnyTimes()
	return q, variantID, versionID, ownerID
}

// toolsDevices are the devices of a deployed topology that are the platform's: the tools devices and the control gateway.
func toolsDevices(topo exerciseModel.Topology) (tools []exerciseModel.Device, controls int) {
	for _, d := range topo.Devices {
		if d.Name == exerciseModel.ControlGatewayName {
			controls++
		}
		if strings.HasPrefix(d.Name, exerciseModel.ToolsDeviceName) && d.Type == exerciseModel.DeviceTypeNode {
			tools = append(tools, d)
		}
	}
	return tools, controls
}

func TestDeployVariantTestWithTools_SeveralDevicesShareOneControlInTheOrderOfThePlacements(t *testing.T) {
	q, variantID, versionID, owner := toolsFixture(t)
	infra := &fakeInfra{status: exerciseModel.LabDeployStatus{VPNConfig: "author-config"}}
	uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Infra: infra, VPNStore: &fakeVPNStore{}})
	uc.SetTestLabGate(&fakeGate{})
	uc.SetToolsImage("platform/tools:1")
	places := []exerciseModel.ToolsPlacement{
		{Env: exerciseModel.EDAEnv("run-a", "")},
		{Image: "brain/opponent:2", Env: exerciseModel.EDAEnv("run-b", ""), Pull: &exerciseModel.ToolsPull{Registry: "brain.example", Username: "u", Password: "p"}},
	}
	handle, err := uc.DeployVariantTestWithTools(context.Background(), owner, versionID, variantID, 0, places)
	if err != nil {
		t.Fatal(err)
	}
	if len(handle.Tools) != 2 || handle.Tools[0] != "tools" || handle.Tools[1] != "tools-2" {
		t.Fatalf("the handle names the tools devices in the order of the placements: %+v", handle.Tools)
	}
	tools, controls := toolsDevices(infra.deployedTopo)
	if len(tools) != 2 || controls != 1 {
		t.Fatalf("two tools devices, one control gateway: %d %d", len(tools), controls)
	}
	for i, d := range tools {
		if d.Interfaces[1].IP.AddressRef.Host != exerciseModel.ControlHost(i) || d.Bootstrap.Env[0].Value != []string{"run-a", "run-b"}[i] {
			t.Fatalf("device %d: %+v", i, d)
		}
	}
	if tools[0].Image != "platform/tools:1" || tools[1].Image != "brain/opponent:2" {
		t.Fatalf("the fallback image and the brain's image as it is: %q %q", tools[0].Image, tools[1].Image)
	}
	if len(infra.meta.PullCredentials) != 1 || infra.meta.PullCredentials[0].Registry != "brain.example" {
		t.Fatalf("the brain's pull credential is the lab's: %+v", infra.meta.PullCredentials)
	}
}

func TestDeployVariantTestWithTools_RefusesMoreThanMaxOpponentsAndAMissingImage(t *testing.T) {
	q, variantID, versionID, owner := toolsFixture(t)
	infra := &fakeInfra{}
	uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Infra: infra, VPNStore: &fakeVPNStore{}})
	uc.SetTestLabGate(&fakeGate{})
	if _, err := uc.DeployVariantTestWithTools(context.Background(), owner, versionID, variantID, 0, []exerciseModel.ToolsPlacement{{Image: "a:1"}, {}}); !errors.Is(err, exerciseModel.ErrToolsNoImage) {
		t.Fatalf("a placement without an image and no fallback: %v", err)
	}
	uc.SetToolsImage("platform/tools:1")
	if _, err := uc.DeployVariantTestWithTools(context.Background(), owner, versionID, variantID, 0, make([]exerciseModel.ToolsPlacement, exerciseModel.MaxOpponents+1)); !errors.Is(err, exerciseModel.ErrToolsTooMany) {
		t.Fatalf("%v", err)
	}
	if infra.deployCalls != 0 {
		t.Fatal("nothing is deployed")
	}
}

// Opponents never play in events: an event preview (and every event lab) is deployed without a tools device or a control
// gateway, even when the platform has a tools image.
func TestAnEventPreviewDeployNeverHasAToolsDeviceOrAControlGateway(t *testing.T) {
	q, variantID, versionID, owner := toolsFixture(t)
	infra := &eventPreviewInfra{fakeInfra: fakeInfra{status: exerciseModel.LabDeployStatus{VPNConfig: "author-config"}}}
	uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Infra: infra, VPNStore: &fakeVPNStore{}})
	uc.SetTestLabGate(&eventPreviewGate{})
	uc.SetToolsImage("platform/tools:1")
	if _, err := uc.DeployEventPreviewVariantTest(context.Background(), uuid.Must(uuid.NewV7()), owner, versionID, variantID, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	if infra.deployCalls != 1 {
		t.Fatalf("the preview lab is deployed: %d", infra.deployCalls)
	}
	tools, controls := toolsDevices(infra.deployedTopo)
	if len(tools) != 0 || controls != 0 || len(infra.deployedTopo.Devices) != 2 {
		t.Fatalf("an event lab is the task's topology only: %+v", infra.deployedTopo.Devices)
	}
}

func TestControlAddressAndTheBotPolicyFollowTheToolsIndex(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := postgresMocks.NewMockQuerier(ctrl)
	owner, deployID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	q.EXPECT().GetOwnedExerciseTestDeploy(gomock.Any(), gomock.Any()).Return(graderRow(owner, deployID), nil).AnyTimes()
	q.EXPECT().ListOwnedExerciseTestDeploys(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	infra := &fakeInfra{status: exerciseModel.LabDeployStatus{VPNConfig: agentConfig("10.8.0.0/10"), Gateways: gws("vpn", "10.8.3.0/24", "ctl", "10.8.4.0/24")}}
	uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Infra: infra})
	for index, want := range []string{"10.8.4.10", "10.8.4.11", "10.8.4.17"} {
		i := []int{0, 1, 7}[index]
		if got, err := uc.ControlAddress(context.Background(), owner, deployID, i); err != nil || got != want {
			t.Fatalf("index %d: %q %v", i, got, err)
		}
	}
	uc.SetLabClientSource(func(context.Context, uuid.UUID) []exercise.LabClient {
		return []exercise.LabClient{{Name: "g-one", DeployID: deployID, Control: true}, {Name: "g-two", DeployID: deployID, Control: true, ToolsIndex: 1}}
	})
	if _, err := uc.MintLabClient(context.Background(), owner, deployID, "g-two"); err != nil {
		t.Fatal(err)
	}
	hosts := map[string]int32{}
	for _, p := range infra.policies {
		if p.Control != nil {
			hosts[p.Name] = p.Control.Host
		}
	}
	if hosts["g-one"] != 10 || hosts["g-two"] != 11 {
		t.Fatalf("each bot reaches only its own device: %v", hosts)
	}
}
