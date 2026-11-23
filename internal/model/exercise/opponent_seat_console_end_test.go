package exerciseModel

import "testing"

// A seat is a serial-port connector only: a device that is the console end of an author's cable is refused as a seat (a bot run
// would drop that cable with the seat).
func TestOpponentSeat_ConsoleEnd(t *testing.T) {
	v := seatVariant(defenseAt("pc1", "db"))
	pc1, idle := deviceNamed(v, "pc1"), deviceNamed(v, "idle")
	pc1.ConsolePorts = []ConsolePort{{Name: "console0"}}
	idle.SerialPorts = append(idle.SerialPorts, SerialPort{Name: "serial1"})
	if err := v.ValidateOpponents(); err != nil {
		t.Fatalf("an uncabled console port is fine: %v", err)
	}
	v.Topology.Connections = append(v.Topology.Connections, Connection{Kind: ConnectionConsole, Endpoints: []Endpoint{
		{Kind: EndpointDevice, DeviceID: idle.ID, Interface: "serial1"}, {Kind: EndpointDevice, DeviceID: pc1.ID, Interface: "console0"},
	}})
	field, reason := fieldOf(t, v.ValidateOpponents())
	if field != "opponents[1].seat" || reason != OpponentReasonSeatConsoleEnd {
		t.Fatalf("got %s %s", field, reason)
	}
}
