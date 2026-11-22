package exerciseModel

import "testing"

// A console cable stays a console cable in the copy, and a defender beside a device lands on its network segment, never on
// its console cable (the first connection of db here).
func TestToolsDevicesKeepConsoleCablesAndSkipThemForPlacement(t *testing.T) {
	db := node("db")
	db.ConsolePorts = []ConsolePort{{Name: "console0"}}
	pc := node("pc")
	pc.SerialPorts = []SerialPort{{Name: "serial0"}}
	topo, vpn, web := toolsBase(t, nil)
	topo.Devices = append(topo.Devices, db, pc)
	topo.Devices[1].Interfaces = append(topo.Devices[1].Interfaces, Interface{Name: "eth1", IP: IPConfig{Type: IPConfigTypeDHCPPreset}})
	consoleCable := Connection{Kind: ConnectionConsole, Endpoints: []Endpoint{port(pc, "serial0"), port(db, "console0")}}
	topo.Connections = []Connection{consoleCable, link(port(vpn, GatewayPort), port(web, "eth0")), link(port(db, "eth0"), port(web, "eth1"))}
	out, tools, err := topo.WithToolsDevices([]ToolsPlacement{{Image: "d:1", Beside: []string{"db"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Connections[0].IsConsole() || out.Connections[0].Endpoints[1] != consoleCable.Endpoints[1] {
		t.Fatalf("the console cable is kept as it is: %+v", out.Connections[0])
	}
	if peerOf(out, tools[0].ID, "eth0").DeviceID != peerOf(out, db.ID, "eth0").DeviceID {
		t.Fatal("the tools device sits on the network segment of db")
	}
	if !topo.Connections[0].IsConsole() {
		t.Fatal("the input topology was changed")
	}
}
