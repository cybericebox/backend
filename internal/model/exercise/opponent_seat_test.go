package exerciseModel

import (
	"testing"

	"github.com/gofrs/uuid"
)

// seatVariant: web and db are operated; pc1 (serial ports) sits on sw beside web, pc2 (serial ports) on its own segment
// sw2 with db; plain has no serial port; idle has serial ports and no network connection.
func seatVariant(opponents ...Opponent) Variant {
	nic := func(names ...string) []Interface {
		var out []Interface
		for _, name := range names {
			out = append(out, Interface{Name: name, IP: IPConfig{Type: IPConfigTypeDHCP}})
		}
		return out
	}
	serial := []SerialPort{{Name: "serial0"}}
	web := Device{ID: uuid.Must(uuid.NewV7()), Name: "web", Type: DeviceTypeNode, Interfaces: nic("eth0")}
	db := Device{ID: uuid.Must(uuid.NewV7()), Name: "db", Type: DeviceTypeNode, Interfaces: nic("eth0"), ConsolePorts: []ConsolePort{{Name: "console0"}}}
	pc1 := Device{ID: uuid.Must(uuid.NewV7()), Name: "pc1", Type: DeviceTypeNode, Interfaces: nic("eth0"), SerialPorts: serial}
	// pc2's first interface has no connection: its first data NIC is eth1, on sw2
	pc2 := Device{ID: uuid.Must(uuid.NewV7()), Name: "pc2", Type: DeviceTypeNode, Interfaces: nic("eth0", "eth1"), SerialPorts: serial}
	plain := Device{ID: uuid.Must(uuid.NewV7()), Name: "plain", Type: DeviceTypeNode, Interfaces: nic("eth0")}
	idle := Device{ID: uuid.Must(uuid.NewV7()), Name: "idle", Type: DeviceTypeNode, Interfaces: nic("eth0"), SerialPorts: serial}
	sw := Device{ID: uuid.Must(uuid.NewV7()), Name: "sw", Type: DeviceTypeUnmanagedSwitch}
	sw2 := Device{ID: uuid.Must(uuid.NewV7()), Name: "sw2", Type: DeviceTypeUnmanagedSwitch}
	ep := func(d Device, port string) Endpoint { return Endpoint{Kind: EndpointDevice, DeviceID: d.ID, Interface: port} }
	return Variant{
		Tasks: []Task{{Key: "root", Name: "Root"}},
		Topology: Topology{
			Devices: []Device{web, db, pc1, pc2, plain, idle, sw, sw2},
			Connections: []Connection{
				{Endpoints: []Endpoint{ep(web, "eth0"), ep(sw, "GigabitEthernet0/1")}},
				{Endpoints: []Endpoint{ep(pc1, "eth0"), ep(sw, "GigabitEthernet0/2")}},
				{Endpoints: []Endpoint{ep(pc2, "eth1"), ep(sw2, "GigabitEthernet0/1")}},
				{Endpoints: []Endpoint{ep(db, "eth0"), ep(sw2, "GigabitEthernet0/2")}},
				{Kind: ConnectionConsole, Endpoints: []Endpoint{ep(pc2, "serial0"), ep(db, "console0")}},
				{Kind: ConnectionConsole, Endpoints: []Endpoint{ep(idle, "serial0"), ep(db, "console0")}},
			},
		},
		Opponents: opponents,
	}
}

func deviceNamed(v Variant, name string) *Device {
	for i := range v.Topology.Devices {
		if v.Topology.Devices[i].Name == name {
			return &v.Topology.Devices[i]
		}
	}
	return nil
}

func defenseAt(seat string, operates ...string) Opponent {
	return Opponent{Role: OpponentDefense, Band: 1, Goal: []string{"root"}, Operates: operates, Seat: seat}
}

func TestOpponentSeat_Valid(t *testing.T) {
	v := seatVariant(
		defenseAt("pc2", "db"),
		Opponent{Role: OpponentAttackDefense, Band: 1, Goal: []string{"root"}, Operates: []string{"web"}, Foothold: "sw", Seat: ""},
		defenseAt("", "web"), // no seat
	)
	if err := v.ValidateOpponents(); err != nil {
		t.Fatal(err)
	}
}

func TestOpponentSeat_Rules(t *testing.T) {
	gatewayVia := func(v *Variant) {
		pc2 := deviceNamed(*v, "pc2")
		pc2.Interfaces[1].IP = IPConfig{Type: IPConfigTypeStatic, Addresses: []string{"10.0.0.5/24"}}
		web := deviceNamed(*v, "web")
		web.Interfaces[0].IP = IPConfig{Type: IPConfigTypeStatic, Addresses: []string{"10.0.0.2/24"}, Gateway: "10.0.0.5"}
	}
	routeVia := func(v *Variant) {
		ref := NetworkIPRef{Gateway: "vpn", Host: 5}
		deviceNamed(*v, "pc2").Interfaces[1].IP = IPConfig{Type: IPConfigTypeStatic, AddressRef: &ref}
		deviceNamed(*v, "web").Interfaces[0].IP = IPConfig{Type: IPConfigTypeStatic, Addresses: []string{"10.0.0.2/24"},
			Routes: []Route{{Dst: "10.9.0.0/16", ViaRef: &NetworkIPRef{Gateway: "vpn", Host: 5}}}}
	}
	checked := func(v *Variant) {
		v.Tasks[0].Check = Check{State: &StateCheck{Conditions: []Condition{{DeviceID: deviceNamed(*v, "pc2").ID}}}}
	}
	for _, c := range []struct {
		name      string
		opponents []Opponent
		change    func(*Variant)
		field     string
		reason    string
	}{
		{"an attacker has no seat", []Opponent{{Role: OpponentAttack, Band: 1, Goal: []string{"root"}, Foothold: "sw", Seat: "pc1"}}, nil, "opponents[1].seat", OpponentReasonSeatForbidden},
		{"unknown device", []Opponent{defenseAt("gone", "db")}, nil, "opponents[1].seat", OpponentReasonSeatUnknown},
		{"a switch is no seat", []Opponent{defenseAt("sw", "db")}, nil, "opponents[1].seat", OpponentReasonSeatUnknown},
		{"no serial port", []Opponent{defenseAt("plain", "db")}, nil, "opponents[1].seat", OpponentReasonSeatNoSerial},
		{"one opponent per seat", []Opponent{defenseAt("pc2", "db"), defenseAt("pc2", "web")}, nil, "opponents[2].seat", OpponentReasonSeatRepeated},
		{"the seat is operated", []Opponent{defenseAt("pc2", "db", "pc2")}, nil, "opponents[1].seat", OpponentReasonSeatOperated},
		{"the seat is operated by another opponent", []Opponent{defenseAt("pc2", "db"), defenseAt("", "pc2")}, nil, "opponents[1].seat", OpponentReasonSeatOperated},
		{"no network connection", []Opponent{defenseAt("idle", "db")}, nil, "opponents[1].seat", OpponentReasonSeatNoNetwork},
		{"on its own foothold segment", []Opponent{{Role: OpponentAttackDefense, Band: 1, Goal: []string{"root"}, Operates: []string{"db"}, Foothold: "sw", Seat: "pc1"}}, nil, "opponents[1].seat", OpponentReasonSeatOnFoothold},
		{"on the foothold segment of another opponent", []Opponent{
			defenseAt("pc2", "db"),
			{Role: OpponentAttack, Band: 1, Goal: []string{"root"}, Foothold: "sw2"},
		}, nil, "opponents[1].seat", OpponentReasonSeatOnFoothold},
		{"observed by a lab check", []Opponent{defenseAt("pc2", "db")}, checked, "opponents[1].seat", OpponentReasonSeatReferenced},
		{"a gateway of another device", []Opponent{defenseAt("pc2", "db")}, gatewayVia, "opponents[1].seat", OpponentReasonSeatReferenced},
		{"a route via its address reference", []Opponent{defenseAt("pc2", "db")}, routeVia, "opponents[1].seat", OpponentReasonSeatReferenced},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := seatVariant(c.opponents...)
			if c.change != nil {
				c.change(&v)
			}
			field, reason := fieldOf(t, v.ValidateOpponents())
			if field != c.field || reason != c.reason {
				t.Fatalf("got %s %s, want %s %s", field, reason, c.field, c.reason)
			}
		})
	}
}

// The document check of a variant (save, import, publish) runs the seat rules.
func TestOpponentSeat_CheckedWithTheDocument(t *testing.T) {
	v := seatVariant(defenseAt("plain", "db"))
	v.Tasks[0].Check = Check{Answer: &AnswerCheck{Type: AnswerTypeFlag}}
	if _, reason := fieldOf(t, v.ValidateDocument()); reason != OpponentReasonSeatNoSerial {
		t.Fatal(reason)
	}
}

func TestCopyOpponents_KeepsTheSeat(t *testing.T) {
	if got := CopyOpponents([]Opponent{{Seat: "pc1"}}); got[0].Seat != "pc1" {
		t.Fatal(got)
	}
}
