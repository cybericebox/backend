package exerciseModel

import (
	"errors"
	"reflect"
	"strconv"
	"testing"

	"github.com/gofrs/uuid"
)

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

// seatLab is a VPN gateway on the lan switch with web and the defender's seat pc1 (eth0 static on lan, eth1 on a second switch);
// pc1's serial0 is cabled to r1/console0 and serial1 (a path of its own) to core/console0; r1 also has aux0, db has console0.
type seatLab struct {
	topo                   Topology
	lan, back              Device
	pc1, r1, core, db, web Device
}

func newSeatLab() seatLab {
	var l seatLab
	vpn := vpnGateway()
	l.lan = Device{ID: uuid.Must(uuid.NewV7()), Name: "lan", Type: DeviceTypeUnmanagedSwitch}
	l.back = Device{ID: uuid.Must(uuid.NewV7()), Name: "back", Type: DeviceTypeUnmanagedSwitch}
	l.web = node("web")
	l.pc1 = node("pc1")
	l.pc1.Interfaces = []Interface{
		{Name: "eth0", IP: IPConfig{Type: IPConfigTypeStatic, Addresses: []string{"10.0.0.5/24"}, Routes: []Route{{Dst: "10.9.0.0/24", Via: "10.0.0.1"}}}},
		{Name: "eth1", IP: IPConfig{Type: IPConfigTypeDHCPPreset}},
	}
	l.pc1.SerialPorts = []SerialPort{{Name: "serial0"}, {Name: "serial1", Path: "/dev/ttyUSB1"}}
	l.r1 = node("r1")
	l.r1.ConsolePorts = []ConsolePort{{Name: "console0", Command: []string{"/usr/bin/vtysh"}}, {Name: "aux0"}}
	l.core = node("core")
	l.core.ConsolePorts = []ConsolePort{{Name: "console0"}}
	l.db = node("db")
	l.db.ConsolePorts = []ConsolePort{{Name: "console0"}}
	l.topo = Topology{
		Devices: []Device{vpn, l.lan, l.back, l.web, l.pc1, l.r1, l.core, l.db},
		Connections: []Connection{
			link(port(vpn, GatewayPort), port(l.lan, "GigabitEthernet0/1")),
			link(port(l.web, "eth0"), port(l.lan, "GigabitEthernet0/2")),
			link(port(l.pc1, "eth0"), port(l.lan, "GigabitEthernet0/3")),
			link(port(l.pc1, "eth1"), port(l.back, "GigabitEthernet0/1")),
			link(port(l.r1, "eth0"), port(l.back, "GigabitEthernet0/2")),
			link(port(l.core, "eth0"), port(l.back, "GigabitEthernet0/3")),
			link(port(l.db, "eth0"), port(l.back, "GigabitEthernet0/4")),
			{Kind: ConnectionConsole, Endpoints: []Endpoint{port(l.pc1, "serial0"), port(l.r1, "console0")}},
			{Kind: ConnectionConsole, Endpoints: []Endpoint{port(l.core, "console0"), port(l.pc1, "serial1")}},
		},
	}
	return l
}

func consoleCablesOf(t Topology) [][2]Endpoint {
	var out [][2]Endpoint
	for _, c := range t.Connections {
		if c.IsConsole() {
			out = append(out, [2]Endpoint{c.Endpoints[0], c.Endpoints[1]})
		}
	}
	return out
}

func connectionsOf(t Topology, id uuid.UUID) []Connection {
	var out []Connection
	for _, c := range t.Connections {
		for _, ep := range c.Endpoints {
			if ep.DeviceID == id {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// With a seat the bot takes its place: the seat is not deployed, the tools device sits on the seat's first data NIC with its IP
// configuration, has the seat's serial ports with the same names and paths and the seat's console cables port for port, and no
// other cable; the seat's other NIC drops with it. An attack-defender's seat comes before its foothold.
func TestToolsDeviceTakesTheSeat(t *testing.T) {
	for name, place := range map[string]ToolsPlacement{
		"defense":        {Image: "d:1", Beside: []string{"r1"}, Seat: "pc1", Operates: []string{"r1", "db"}},
		"attack-defense": {Image: "d:1", Foothold: "back", Seat: "pc1", Operates: []string{"r1", "db"}},
	} {
		t.Run(name, func(t *testing.T) {
			l := newSeatLab()
			out, tools, err := l.topo.WithToolsDevices([]ToolsPlacement{place})
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range out.Devices {
				if d.ID == l.pc1.ID {
					t.Fatal("the seat is not deployed")
				}
			}
			if got := connectionsOf(out, l.pc1.ID); len(got) != 0 {
				t.Fatalf("nothing names the seat: %+v", got)
			}
			if peer := peerOf(out, tools[0].ID, "eth0"); peer != port(l.lan, "GigabitEthernet0/3") {
				t.Fatalf("eth0 takes the seat's first data NIC: %+v", peer)
			}
			if !reflect.DeepEqual(tools[0].Interfaces[0].IP, l.pc1.Interfaces[0].IP) {
				t.Fatalf("eth0 takes its IP configuration: %+v", tools[0].Interfaces[0].IP)
			}
			if !reflect.DeepEqual(tools[0].SerialPorts, l.pc1.SerialPorts) {
				t.Fatalf("the serial ports keep their names and paths: %+v", tools[0].SerialPorts)
			}
			want := [][2]Endpoint{
				{port(tools[0], "serial0"), port(l.r1, "console0")},
				{port(l.core, "console0"), port(tools[0], "serial1")},
			}
			if got := consoleCablesOf(out); !reflect.DeepEqual(got, want) {
				t.Fatalf("the cables are re-pointed port for port and none is added (db stays uncabled): %+v", got)
			}
			if got := connectionsOf(out, l.back.ID); len(got) != 3 {
				t.Fatalf("the seat's eth1 drops: %d connections on back", len(got))
			}
			if !reflect.DeepEqual(l.topo, newSeatLabLike(l)) || len(l.topo.Devices) != 8 || l.topo.Connections[7].Endpoints[0].DeviceID != l.pc1.ID {
				t.Fatal("the input topology was changed")
			}
		})
	}
}

// newSeatLabLike is the lab as it was built, for the check that the input stays as it is.
func newSeatLabLike(l seatLab) Topology { return l.topo }

// Without a seat the tools device is added on top and cabled to the first free console port of each operated device that
// declares one, in operates order: r1's console0 is the author's, so aux0; web has no port; db gets console0. Nothing is
// re-plugged and the human's seat stays as authored.
func TestToolsDeviceIsCabledToFreeConsolePorts(t *testing.T) {
	l := newSeatLab()
	out, tools, err := l.topo.WithToolsDevices([]ToolsPlacement{{Image: "d:1", Beside: []string{"r1"}, Operates: []string{"web", "r1", "db"}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []SerialPort{{Name: "serial0"}, {Name: "serial1"}}; !reflect.DeepEqual(tools[0].SerialPorts, want) {
		t.Fatalf("serial ports in operates order: %+v", tools[0].SerialPorts)
	}
	want := [][2]Endpoint{
		{port(l.pc1, "serial0"), port(l.r1, "console0")},
		{port(l.core, "console0"), port(l.pc1, "serial1")},
		{port(tools[0], "serial0"), port(l.r1, "aux0")},
		{port(tools[0], "serial1"), port(l.db, "console0")},
	}
	if got := consoleCablesOf(out); !reflect.DeepEqual(got, want) {
		t.Fatalf("cables: %+v", got)
	}

	// A second defender gets what is left: r1 has no free port any more, db neither; core's console0 is the author's.
	out, tools, err = l.topo.WithToolsDevices([]ToolsPlacement{
		{Image: "d:1", Beside: []string{"r1"}, Operates: []string{"r1", "db"}},
		{Image: "d:1", Beside: []string{"r1"}, Operates: []string{"r1", "db", "core"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools[1].SerialPorts) != 0 || len(consoleCablesOf(out)) != 4 {
		t.Fatalf("no free port, no cable: %+v %+v", tools[1].SerialPorts, consoleCablesOf(out))
	}

	// An attacker (and a grader) operates nothing: no serial port, no cable.
	_, tools, err = l.topo.WithToolsDevices([]ToolsPlacement{{Image: "a:1", Foothold: "lan"}})
	if err != nil || len(tools[0].SerialPorts) != 0 {
		t.Fatalf("attacker: %v %+v", err, tools[0].SerialPorts)
	}
}

// More console lines than a device may have serial ports is refused, not cut short.
func TestToolsDeviceSerialPortCap(t *testing.T) {
	topo, _, _ := toolsBase(t, func(vpn, web Device) []Connection {
		return []Connection{link(port(vpn, GatewayPort), port(web, "eth0"))}
	})
	var operates []string
	for i := range MaxSerialPorts + 1 {
		d := node("r" + strconv.Itoa(i))
		d.ConsolePorts = []ConsolePort{{Name: "console0"}}
		topo.Devices = append(topo.Devices, d)
		operates = append(operates, d.Name)
	}
	if _, _, err := topo.WithToolsDevices([]ToolsPlacement{{Image: "d:1", Beside: []string{"web"}, Operates: operates}}); !errors.Is(err, ErrToolsNoSerialPort) {
		t.Fatalf("over the cap: %v", err)
	}
	if _, _, err := topo.WithToolsDevices([]ToolsPlacement{{Image: "d:1", Beside: []string{"web"}, Operates: operates[:MaxSerialPorts]}}); err != nil {
		t.Fatalf("at the cap: %v", err)
	}
}

func TestToolsDeviceSeatRefusals(t *testing.T) {
	l := newSeatLab()
	for _, seat := range []string{"nobody", "lan"} {
		if _, _, err := l.topo.WithToolsDevices([]ToolsPlacement{{Image: "d:1", Seat: seat}}); !errors.Is(err, ErrToolsNoSeat) {
			t.Fatalf("seat %q: %v", seat, err)
		}
	}
	lone := l.topo
	lone.Connections = nil
	if _, _, err := lone.WithToolsDevices([]ToolsPlacement{{Image: "d:1", Seat: "pc1"}}); !errors.Is(err, ErrToolsNoSeat) {
		t.Fatalf("a seat without a network connection: %v", err)
	}
}
