package exerciseModel

import (
	"errors"
	"testing"

	"github.com/gofrs/uuid"
)

func dhcpRange() []DHCPRange { return []DHCPRange{{Start: 100, End: 200}} }

func node(name string) Device {
	return Device{ID: uuid.Must(uuid.NewV7()), Name: name, Type: DeviceTypeNode, Profile: NodeProfileContainer, Image: name + ":1",
		Interfaces: []Interface{{Name: "eth0", IP: IPConfig{Type: IPConfigTypeDHCPPreset}}}}
}

func vpnGateway() Device {
	return Device{ID: uuid.Must(uuid.NewV7()), Name: "vpn", Type: DeviceTypeVPNGateway, DHCP: true, DHCPRanges: dhcpRange()}
}

func port(d Device, name string) Endpoint {
	return Endpoint{Kind: EndpointDevice, DeviceID: d.ID, Interface: name}
}

func link(a, b Endpoint) Connection { return Connection{Endpoints: []Endpoint{a, b}} }

func toolsBase(t *testing.T, cable func(vpn, web Device) []Connection) (Topology, Device, Device) {
	t.Helper()
	vpn, web := vpnGateway(), node("web")
	topo := Topology{Devices: []Device{vpn, web}}
	if cable != nil {
		topo.Connections = cable(vpn, web)
	}
	return topo, vpn, web
}

func TestWithToolsDeviceBehindANode(t *testing.T) {
	topo, vpn, web := toolsBase(t, func(vpn, web Device) []Connection {
		return []Connection{link(port(vpn, GatewayPort), port(web, "eth0"))}
	})
	out, tools, err := topo.WithToolsDevice("tools:1", ToolsPlacement{})
	if err != nil {
		t.Fatal(err)
	}
	if tools.Internet != InternetDeny {
		t.Fatalf("the tools device is denied the internet explicitly: %q", tools.Internet)
	}
	if tools.Internet != InternetDeny {
		t.Fatalf("the tools device is denied the internet explicitly: %q", tools.Internet)
	}
	if tools.Name != "tools" || len(tools.Access) != 0 || tools.Image != "tools:1" {
		t.Fatalf("tools = %+v", tools)
	}
	// the tools device and a switch between the gateway and the node
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
	out, _, err := topo.WithToolsDevice("tools:1", ToolsPlacement{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Devices) != 4 || len(out.Connections) != 3 || out.Connections[2].Endpoints[1].Interface != "GigabitEthernet0/3" {
		t.Fatalf("the device takes the next free port of the gateway's switch: %+v", out.Connections)
	}

	bare, _, _ := toolsBase(t, nil)
	if out, _, err = bare.WithToolsDevice("tools:1", ToolsPlacement{}); err != nil || len(out.Connections) != 1 {
		t.Fatalf("uncabled: %v %+v", err, out.Connections)
	}
}

func TestWithToolsDeviceRefusalsAndName(t *testing.T) {
	topo, _, web := toolsBase(t, nil)
	if _, _, err := topo.WithToolsDevice("", ToolsPlacement{}); !errors.Is(err, ErrToolsNoImage) {
		t.Fatalf("no image: %v", err)
	}
	noVPN := Topology{Devices: []Device{web}}
	if _, _, err := noVPN.WithToolsDevice("tools:1", ToolsPlacement{}); !errors.Is(err, ErrToolsNoVPN) {
		t.Fatalf("no vpn: %v", err)
	}
	noDHCP := topo
	noDHCP.Devices = []Device{{ID: uuid.Must(uuid.NewV7()), Name: "vpn", Type: DeviceTypeVPNGateway}, web}
	if _, _, err := noDHCP.WithToolsDevice("tools:1", ToolsPlacement{}); !errors.Is(err, ErrToolsNoVPN) {
		t.Fatalf("a VPN gateway without DHCP: %v", err)
	}
	taken := topo
	taken.Devices = []Device{topo.Devices[0], node("tools")}
	_, tools, err := taken.WithToolsDevice("tools:1", ToolsPlacement{})
	if err != nil || tools.Name != "tools-2" {
		t.Fatalf("name = %q err %v", tools.Name, err)
	}
}

func TestWithToolsDevicePlacement(t *testing.T) {
	sw := Device{ID: uuid.Must(uuid.NewV7()), Name: "dmz", Type: DeviceTypeUnmanagedSwitch}
	db := node("db")
	topo, vpn, web := toolsBase(t, nil)
	topo.Devices = append(topo.Devices, sw, db)
	topo.Devices[1].Interfaces = append(topo.Devices[1].Interfaces, Interface{Name: "eth1", IP: IPConfig{Type: IPConfigTypeDHCPPreset}})
	topo.Connections = []Connection{
		link(port(vpn, GatewayPort), port(sw, "GigabitEthernet0/1")),
		link(port(web, "eth0"), port(sw, "GigabitEthernet0/2")),
		link(port(db, "eth0"), port(web, "eth1")),
	}

	t.Run("the foothold is a port of that switch", func(t *testing.T) {
		out, tools, err := topo.WithToolsDevice("tools:1", ToolsPlacement{Foothold: "dmz"})
		if err != nil {
			t.Fatal(err)
		}
		var onFoothold bool
		for _, c := range out.Connections {
			if c.Endpoints[0].DeviceID == tools.ID && c.Endpoints[0].Interface == "eth0" && c.Endpoints[1].DeviceID == sw.ID && c.Endpoints[1].Interface == "GigabitEthernet0/3" {
				onFoothold = true
			}
		}
		if !onFoothold {
			t.Fatalf("%+v", out.Connections)
		}
	})
	t.Run("beside an operated device with a node peer puts a switch between them", func(t *testing.T) {
		out, _, err := topo.WithToolsDevice("tools:1", ToolsPlacement{Beside: []string{"db"}})
		if err != nil {
			t.Fatal(err)
		}
		// the tools device and a switch for the segment
		if len(out.Devices) != len(topo.Devices)+2 || len(out.Connections) != len(topo.Connections)+2 {
			t.Fatalf("devices %d connections %d", len(out.Devices), len(out.Connections))
		}
	})
	t.Run("beside a device on a switch takes a port of that switch", func(t *testing.T) {
		out, _, err := topo.WithToolsDevice("tools:1", ToolsPlacement{Beside: []string{"web"}})
		if err != nil || len(out.Devices) != len(topo.Devices)+1 {
			t.Fatalf("%v %d", err, len(out.Devices))
		}
	})
	t.Run("an unknown or non-switch foothold, an unknown device", func(t *testing.T) {
		for _, p := range []ToolsPlacement{{Foothold: "nope"}, {Foothold: "web"}, {Beside: []string{"nope"}}} {
			if _, _, err := topo.WithToolsDevice("tools:1", p); !errors.Is(err, ErrToolsNoSegment) {
				t.Fatalf("%+v: %v", p, err)
			}
		}
	})
	t.Run("the run's variables reach the device", func(t *testing.T) {
		_, tools, err := topo.WithToolsDevice("tools:1", ToolsPlacement{Env: EDAEnv("run-1", "-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----\n")})
		if err != nil || len(tools.Bootstrap.Env) != 2 || tools.Bootstrap.Env[0].Name != EnvEDARunID || tools.Bootstrap.Env[0].Value != "run-1" || tools.Bootstrap.Env[1].Name != EnvEDATrustCA {
			t.Fatalf("%v %+v", err, tools.Bootstrap)
		}
	})
}

func TestAnInternetPolicyIsAllowOrDenyAndNotForASwitch(t *testing.T) {
	topo, _, _ := toolsBase(t, nil)
	topo.Devices[1].Internet = "sometimes"
	if err := topo.validateStructure(); err == nil {
		t.Fatal("an unknown policy must be refused")
	}
	topo.Devices[1].Internet = InternetDeny
	if err := topo.validateStructure(); err != nil {
		t.Fatal(err)
	}
	topo.Devices = append(topo.Devices, Device{ID: uuid.Must(uuid.NewV7()), Name: "sw", Type: DeviceTypeUnmanagedSwitch, Internet: InternetDeny})
	if err := topo.validateStructure(); err == nil {
		t.Fatal("a switch has no internet policy")
	}
}
