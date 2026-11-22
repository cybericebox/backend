package exercise

import (
	"reflect"
	"testing"

	"github.com/gofrs/uuid"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

// The ports of a device and the kind of a connection go through the editor's DTO both ways, so a save never drops them.
func TestTopologyDTOCarriesConsolePortsAndCablesBothWays(t *testing.T) {
	r1 := exerciseModel.Device{ID: uuid.Must(uuid.NewV7()), Name: "r1", Type: exerciseModel.DeviceTypeNode, Profile: exerciseModel.NodeProfileContainer,
		ConsolePorts: []exerciseModel.ConsolePort{
			{Name: "console0", Command: []string{"/usr/bin/vtysh"}, User: "frr"},
			{Name: "aux0", Native: &exerciseModel.NativeSource{Kind: exerciseModel.NativeTCP, Port: 2001}},
		}}
	pc := exerciseModel.Device{ID: uuid.Must(uuid.NewV7()), Name: "pc", Type: exerciseModel.DeviceTypeNode, Profile: exerciseModel.NodeProfileContainer,
		SerialPorts: []exerciseModel.SerialPort{{Name: "serial0"}, {Name: "serial1", Path: "/dev/ttyUSB0"}}}
	in := exerciseModel.Topology{
		Devices: []exerciseModel.Device{r1, pc},
		Connections: []exerciseModel.Connection{{Kind: exerciseModel.ConnectionConsole, Endpoints: []exerciseModel.Endpoint{
			{Kind: exerciseModel.EndpointDevice, DeviceID: pc.ID, Interface: "serial0"},
			{Kind: exerciseModel.EndpointDevice, DeviceID: r1.ID, Interface: "console0"},
		}}},
	}
	out := topologyToDTO(in).toDomain()
	if !reflect.DeepEqual(out.Devices[0].ConsolePorts, r1.ConsolePorts) || !reflect.DeepEqual(out.Devices[1].SerialPorts, pc.SerialPorts) {
		t.Fatalf("ports: %+v %+v", out.Devices[0].ConsolePorts, out.Devices[1].SerialPorts)
	}
	if !out.Connections[0].IsConsole() {
		t.Fatalf("connection kind: %+v", out.Connections[0])
	}
}

func TestOpponentDTOCarriesTheSeatBothWays(t *testing.T) {
	v := exerciseModel.Variant{Opponents: []exerciseModel.Opponent{{Role: exerciseModel.OpponentDefense, Seat: "pc1"}}}
	dto := variantToDTO(v)
	if dto.Opponents[0].Seat != "pc1" || dto.toDomain().Opponents[0].Seat != "pc1" {
		t.Fatalf("seat = %+v", dto.Opponents)
	}
}
