package exerciseModel

import (
	"errors"
	"testing"

	"github.com/gofrs/uuid"
)

func toolsBase(t *testing.T, cable func(vpn, web Device) []Connection) (Topology, Device, Device) {
	t.Helper()
	vpn := Device{ID: uuid.Must(uuid.NewV7()), Name: "vpn", Type: DeviceTypeVPNGateway, DHCP: true, DHCPRanges: []DHCPRange{{Start: 100, End: 200}}}
	web := Device{ID: uuid.Must(uuid.NewV7()), Name: "web", Type: DeviceTypeNode, Profile: NodeProfileContainer, Image: "web:1",
		Interfaces: []Interface{{Name: "eth0", IP: IPConfig{Type: IPConfigTypeDHCPPreset}}}}
	topo := Topology{Devices: []Device{vpn, web}}
	if cable != nil {
		topo.Connections = cable(vpn, web)
	}
	return topo, vpn, web
}

func port(d Device, name string) Endpoint {
	return Endpoint{Kind: EndpointDevice, DeviceID: d.ID, Interface: name}
}

func link(a, b Endpoint) Connection { return Connection{Endpoints: []Endpoint{a, b}} }

func TestWithToolsDeviceBehindANode(t *testing.T) {
	topo, vpn, web := toolsBase(t, func(vpn, web Device) []Connection {
		return []Connection{link(port(vpn, GatewayPort), port(web, "eth0"))}
	})
	out, tools, err := topo.WithToolsDevice("tools:1")
	if err != nil {
		t.Fatal(err)
	}
	if tools.Name != "tools" || len(tools.Access) != 1 || tools.Access[0].Kind != AccessGRPC || tools.Image != "tools:1" {
		t.Fatalf("tools = %+v", tools)
	}
	if len(out.Devices) != 4 || len(out.Connections) != 3 {
		t.Fatalf("devices %d connections %d", len(out.Devices), len(out.Connections))
	}
	for _, d := range out.Devices {
		if d.Type == DeviceTypeInternetGateway {
			t.Fatal("the tools device must not be cabled to the internet")
		}
	}
	if len(topo.Devices) != 2 || len(topo.Connections) != 1 || topo.Connections[0].Endpoints[1].DeviceID != web.ID || topo.Connections[0].Endpoints[0].DeviceID != vpn.ID {
		t.Fatal("the input topology was changed")
	}
}

func TestWithToolsDeviceOnASwitchAndUncabled(t *testing.T) {
	sw := Device{ID: uuid.Must(uuid.NewV7()), Name: "lan", Type: DeviceTypeUnmanagedSwitch}
	topo, vpn, web := toolsBase(t, nil)
	topo.Devices = append(topo.Devices, sw)
	topo.Connections = []Connection{
		link(port(vpn, GatewayPort), port(sw, "GigabitEthernet0/1")),
		link(port(web, "eth0"), port(sw, "GigabitEthernet0/2")),
	}
	out, _, err := topo.WithToolsDevice("tools:1")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Devices) != 4 || len(out.Connections) != 3 || out.Connections[2].Endpoints[1].Interface != "GigabitEthernet0/3" {
		t.Fatalf("out = %+v", out.Connections)
	}
	bare, _, _ := toolsBase(t, nil)
	if out, _, err = bare.WithToolsDevice("tools:1"); err != nil || len(out.Connections) != 1 {
		t.Fatalf("uncabled: %v %+v", err, out.Connections)
	}
}

func TestWithToolsDeviceRefusalsAndName(t *testing.T) {
	topo, _, web := toolsBase(t, nil)
	if _, _, err := topo.WithToolsDevice(""); !errors.Is(err, ErrToolsNoImage) {
		t.Fatalf("no image: %v", err)
	}
	noVPN := Topology{Devices: []Device{web}}
	if _, _, err := noVPN.WithToolsDevice("tools:1"); !errors.Is(err, ErrToolsNoVPN) {
		t.Fatalf("no vpn: %v", err)
	}
	taken := topo
	taken.Devices = append([]Device(nil), topo.Devices...)
	taken.Devices[1].Name = "tools"
	_, tools, err := taken.WithToolsDevice("tools:1")
	if err != nil || tools.Name != "tools-2" {
		t.Fatalf("name = %q err %v", tools.Name, err)
	}
}
