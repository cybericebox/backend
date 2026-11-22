package exerciseModel

import (
	"errors"
	"strings"
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
	if tools.Name != "tools" || len(tools.Access) != 0 || tools.Image != "tools:1" {
		t.Fatalf("tools = %+v", tools)
	}
	// the tools device, a switch between the gateway and the node, the control gateway and its switch
	if len(out.Devices) != 6 || len(out.Connections) != 5 {
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
	if len(out.Devices) != 6 || len(out.Connections) != 5 || out.Connections[2].Endpoints[1].Interface != "GigabitEthernet0/3" {
		t.Fatalf("the device takes the next free port of the gateway's switch: %+v", out.Connections)
	}

	bare, _, _ := toolsBase(t, nil)
	if out, _, err = bare.WithToolsDevice("tools:1", ToolsPlacement{}); err != nil || len(out.Connections) != 3 {
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
	clash := topo
	clash.Devices = []Device{topo.Devices[0], node("ctl")}
	if _, _, err := clash.WithToolsDevice("tools:1", ToolsPlacement{}); !errors.Is(err, ErrToolsNoControl) {
		t.Fatalf("an author's device named ctl: %v", err)
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
		// the tools device, a switch for the segment, the control gateway and its switch
		if len(out.Devices) != len(topo.Devices)+4 || len(out.Connections) != len(topo.Connections)+4 {
			t.Fatalf("devices %d connections %d", len(out.Devices), len(out.Connections))
		}
	})
	t.Run("beside a device on a switch takes a port of that switch", func(t *testing.T) {
		out, _, err := topo.WithToolsDevice("tools:1", ToolsPlacement{Beside: []string{"web"}})
		if err != nil || len(out.Devices) != len(topo.Devices)+3 {
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

func TestAToolsDeviceHasAControlGatewayWithAFixedAddress(t *testing.T) {
	topo, vpn, web := toolsBase(t, func(vpn, web Device) []Connection {
		return []Connection{link(port(vpn, GatewayPort), port(web, "eth0"))}
	})
	out, tools, err := topo.WithToolsDevice("tools:1", ToolsPlacement{})
	if err != nil {
		t.Fatal(err)
	}
	var ctl Device
	for _, d := range out.Devices {
		if d.Name == ControlGatewayName {
			ctl = d
		}
	}
	if ctl.Type != DeviceTypeVPNGateway || ctl.DHCP || len(ctl.DHCPRanges) != 0 || ctl.ReverseConnections {
		t.Fatalf("a VPN gateway of its own, no DHCP, no reverse connections: %+v", ctl)
	}
	for _, d := range topo.Devices {
		if d.Name == ControlGatewayName {
			t.Fatal("the input has no control gateway")
		}
	}
	if len(tools.Interfaces) != 2 || tools.Interfaces[0].Name != "eth0" || tools.Interfaces[0].IP.Type != IPConfigTypeDHCPPreset {
		t.Fatalf("the first interface stays where its placement put it: %+v", tools.Interfaces)
	}
	second := tools.Interfaces[1]
	if second.Name != "eth1" || second.IP.Type != IPConfigTypeStatic || second.IP.AddressRef == nil || second.IP.AddressRef.Gateway != ControlGatewayName || second.IP.AddressRef.Host != ControlHost(0) || ControlHost(0) != 10 {
		t.Fatalf("the second interface has the fixed address on ctl: %+v", second)
	}
	// ctl - switch - eth1: the gateway's connection ends at a switch that the device's second interface is cabled to
	var gatewayPort, devicePort Endpoint
	for _, c := range out.Connections {
		for i, ep := range c.Endpoints {
			if ep.DeviceID == ctl.ID {
				gatewayPort = c.Endpoints[1-i]
			}
			if ep.DeviceID == tools.ID && ep.Interface == "eth1" {
				devicePort = c.Endpoints[1-i]
			}
		}
	}
	if gatewayPort.DeviceID == uuid.Nil || gatewayPort.DeviceID != devicePort.DeviceID {
		t.Fatalf("the gateway and the second interface meet at one switch: %+v %+v", gatewayPort, devicePort)
	}
	// the author's own devices are untouched
	for _, d := range out.Devices {
		if d.ID == web.ID && len(d.Interfaces) != 1 {
			t.Fatalf("the author's device has no control interface: %+v", d)
		}
		if d.ID == vpn.ID && d.DHCP != true {
			t.Fatalf("%+v", d)
		}
	}
}

func TestTheControlNamesAreReservedAtValidation(t *testing.T) {
	for _, c := range []struct {
		name string
		kind DeviceType
	}{{"ctl", DeviceTypeNode}, {"CTL", DeviceTypeNode}, {"ctl", DeviceTypeVPNGateway}, {"ctl", DeviceTypeInternetGateway}, {"ctl-switch", DeviceTypeUnmanagedSwitch}, {"ctl-switch", DeviceTypeHub}, {" ctl ", DeviceTypeNode}} {
		variant := Variant{Topology: Topology{Devices: []Device{{ID: uuid.Must(uuid.NewV7()), Name: c.name, Type: c.kind}}}}
		if err := ValidateDeviceNames([]Variant{variant}); !errors.Is(err, ErrDeviceNameReserved.Err()) {
			t.Errorf("%q (%s): %v", c.name, c.kind, err)
		}
	}
	ok := Variant{Topology: Topology{Devices: []Device{{ID: uuid.Must(uuid.NewV7()), Name: "ctl2", Type: DeviceTypeNode}, {ID: uuid.Must(uuid.NewV7()), Name: "web", Type: DeviceTypeNode}}}}
	if err := ValidateDeviceNames([]Variant{ok}); err != nil {
		t.Fatalf("other names are free: %v", err)
	}
}

// controlOf finds the platform's control gateway and the switch the gateway is cabled to.
func controlOf(t *testing.T, topo Topology) (gateway Device, sw uuid.UUID) {
	t.Helper()
	n := 0
	for _, d := range topo.Devices {
		if d.Name == ControlGatewayName {
			gateway = d
			n++
		}
	}
	if n != 1 {
		t.Fatalf("one control gateway, got %d", n)
	}
	for _, c := range topo.Connections {
		for i, ep := range c.Endpoints {
			if ep.DeviceID == gateway.ID {
				sw = c.Endpoints[1-i].DeviceID
			}
		}
	}
	return gateway, sw
}

// peerOf is the endpoint a device's interface is cabled to.
func peerOf(topo Topology, id uuid.UUID, iface string) Endpoint {
	for _, c := range topo.Connections {
		for i, ep := range c.Endpoints {
			if ep.DeviceID == id && ep.Interface == iface {
				return c.Endpoints[1-i]
			}
		}
	}
	return Endpoint{}
}

func TestSeveralToolsDevicesShareOneControlGateway(t *testing.T) {
	topo, _, _ := toolsBase(t, func(vpn, web Device) []Connection {
		return []Connection{link(port(vpn, GatewayPort), port(web, "eth0"))}
	})
	out, tools, err := topo.WithToolsDevices([]ToolsPlacement{{Image: "a:1"}, {Image: "b:1"}, {Image: "c:1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 3 || tools[0].Name != "tools" || tools[1].Name != "tools-2" || tools[2].Name != "tools-3" || tools[1].Image != "b:1" {
		t.Fatalf("the devices in the order of the placements: %+v", tools)
	}
	gateway, sw := controlOf(t, out)
	switches := 0
	for _, d := range out.Devices {
		if d.Name == controlSwitchName {
			switches++
		}
	}
	if switches != 1 {
		t.Fatalf("one control switch: %d", switches)
	}
	ports := map[string]bool{}
	for i, d := range tools {
		eth1 := d.Interfaces[1]
		if eth1.Name != "eth1" || eth1.IP.AddressRef == nil || eth1.IP.AddressRef.Gateway != ControlGatewayName || eth1.IP.AddressRef.Host != ControlHost(i) {
			t.Fatalf("device %d has the host of its index: %+v", i, eth1)
		}
		p := peerOf(out, d.ID, "eth1")
		if p.DeviceID != sw || ports[p.Interface] {
			t.Fatalf("device %d is on a port of its own of the control switch: %+v", i, p)
		}
		ports[p.Interface] = true
	}
	if p := peerOf(out, gateway.ID, GatewayPort); p.DeviceID != sw || p.Interface != "GigabitEthernet0/1" {
		t.Fatalf("the gateway is on the first port: %+v", p)
	}
}

func TestOneToolsDeviceLooksAsBefore(t *testing.T) {
	topo, _, _ := toolsBase(t, func(vpn, web Device) []Connection {
		return []Connection{link(port(vpn, GatewayPort), port(web, "eth0"))}
	})
	one, single, err := topo.WithToolsDevice("tools:1", ToolsPlacement{})
	if err != nil {
		t.Fatal(err)
	}
	many, list, err := topo.WithToolsDevices([]ToolsPlacement{{Image: "tools:1"}})
	if err != nil || len(list) != 1 {
		t.Fatal(err)
	}
	if deviceNameList(one) != deviceNameList(many) || len(one.Connections) != len(many.Connections) || single.Name != list[0].Name {
		t.Fatalf("%s vs %s", deviceNameList(one), deviceNameList(many))
	}
}

func deviceNameList(t Topology) string {
	s := ""
	for _, d := range t.Devices {
		s += d.Name + ","
	}
	return s
}

func TestTwoAttackersOnOneFootholdTakeDistinctPorts(t *testing.T) {
	sw := Device{ID: uuid.Must(uuid.NewV7()), Name: "dmz", Type: DeviceTypeUnmanagedSwitch}
	topo, vpn, web := toolsBase(t, nil)
	topo.Devices = append(topo.Devices, sw)
	topo.Connections = []Connection{link(port(vpn, GatewayPort), port(sw, "GigabitEthernet0/1")), link(port(web, "eth0"), port(sw, "GigabitEthernet0/2"))}
	out, tools, err := topo.WithToolsDevices([]ToolsPlacement{{Image: "a:1", Foothold: "dmz"}, {Image: "a:1", Foothold: "dmz"}})
	if err != nil {
		t.Fatal(err)
	}
	a, b := peerOf(out, tools[0].ID, "eth0"), peerOf(out, tools[1].ID, "eth0")
	if a.DeviceID != sw.ID || b.DeviceID != sw.ID || a.Interface == b.Interface || a.Interface != "GigabitEthernet0/3" || b.Interface != "GigabitEthernet0/4" {
		t.Fatalf("%+v %+v", a, b)
	}
}

func TestTwoDefendersBesideOneDeviceShareTheInsertedSwitch(t *testing.T) {
	db := node("db")
	topo, vpn, web := toolsBase(t, nil)
	topo.Devices = append(topo.Devices, db)
	topo.Devices[1].Interfaces = append(topo.Devices[1].Interfaces, Interface{Name: "eth1", IP: IPConfig{Type: IPConfigTypeDHCPPreset}})
	topo.Connections = []Connection{link(port(vpn, GatewayPort), port(web, "eth0")), link(port(db, "eth0"), port(web, "eth1"))}
	out, tools, err := topo.WithToolsDevices([]ToolsPlacement{{Image: "d:1", Beside: []string{"db"}}, {Image: "d:1", Beside: []string{"db"}}})
	if err != nil {
		t.Fatal(err)
	}
	var inserted []Device
	for _, d := range out.Devices {
		if d.Type == DeviceTypeUnmanagedSwitch && d.Name != controlSwitchName {
			inserted = append(inserted, d)
		}
	}
	if len(inserted) != 1 || inserted[0].Name != toolsSwitchName {
		t.Fatalf("one tools switch for the segment: %+v", inserted)
	}
	sw := inserted[0].ID
	dbPeer, webPeer := peerOf(out, db.ID, "eth0"), peerOf(out, web.ID, "eth1")
	a, b := peerOf(out, tools[0].ID, "eth0"), peerOf(out, tools[1].ID, "eth0")
	if dbPeer.DeviceID != sw || webPeer.DeviceID != sw || a.DeviceID != sw || b.DeviceID != sw {
		t.Fatalf("the segment and both devices meet at the switch: %+v %+v %+v %+v", dbPeer, webPeer, a, b)
	}
	if used := map[string]bool{dbPeer.Interface: true, webPeer.Interface: true, a.Interface: true, b.Interface: true}; len(used) != 4 {
		t.Fatalf("every end on a port of its own: %v", used)
	}
}

func TestDefendersBesideTwoDevicesGetSwitchesWithTheirOwnNames(t *testing.T) {
	db, cache := node("db"), node("cache")
	topo, vpn, web := toolsBase(t, nil)
	topo.Devices = append(topo.Devices, db, cache)
	topo.Devices[1].Interfaces = append(topo.Devices[1].Interfaces, Interface{Name: "eth1", IP: IPConfig{Type: IPConfigTypeDHCPPreset}}, Interface{Name: "eth2", IP: IPConfig{Type: IPConfigTypeDHCPPreset}})
	topo.Connections = []Connection{link(port(vpn, GatewayPort), port(web, "eth0")), link(port(db, "eth0"), port(web, "eth1")), link(port(cache, "eth0"), port(web, "eth2"))}
	out, _, err := topo.WithToolsDevices([]ToolsPlacement{{Image: "d:1", Beside: []string{"db"}}, {Image: "d:1", Beside: []string{"cache"}}})
	if err != nil {
		t.Fatal(err)
	}
	names := deviceNameList(out)
	if !strings.Contains(names, toolsSwitchName+",") || !strings.Contains(names, toolsSwitchName+"-2,") {
		t.Fatalf("%s", names)
	}
}

func TestTheToolsDevicesAreCappedAndATasksControlGatewayIsRefused(t *testing.T) {
	topo, _, _ := toolsBase(t, nil)
	places := make([]ToolsPlacement, MaxOpponents+1)
	for i := range places {
		places[i] = ToolsPlacement{Image: "a:1"}
	}
	if _, _, err := topo.WithToolsDevices(places); !errors.Is(err, ErrToolsTooMany) {
		t.Fatalf("more than MaxOpponents: %v", err)
	}
	out, tools, err := topo.WithToolsDevices(places[:MaxOpponents])
	if err != nil || len(tools) != MaxOpponents || tools[MaxOpponents-1].Interfaces[1].IP.AddressRef.Host != ControlHost(MaxOpponents-1) {
		t.Fatalf("MaxOpponents fit: %v", err)
	}
	controlOf(t, out)
	if _, _, err := topo.WithToolsDevices([]ToolsPlacement{{Image: "a:1"}, {}}); !errors.Is(err, ErrToolsNoImage) {
		t.Fatalf("every device has an image: %v", err)
	}
	clash := topo
	clash.Devices = []Device{topo.Devices[0], node("ctl")}
	if _, _, err := clash.WithToolsDevices([]ToolsPlacement{{Image: "a:1"}, {Image: "b:1"}}); !errors.Is(err, ErrToolsNoControl) {
		t.Fatalf("a task's own ctl: %v", err)
	}
	// a topology that holds the platform's control already (a second call) is refused like a task's own ctl: all the
	// tools devices of a lab are added in one call
	if _, _, err := out.WithToolsDevices([]ToolsPlacement{{Image: "a:1"}}); !errors.Is(err, ErrToolsNoControl) {
		t.Fatalf("a second call: %v", err)
	}
}
