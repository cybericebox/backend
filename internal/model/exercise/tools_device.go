package exerciseModel

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/gofrs/uuid"
)

// Why the platform tools device cannot be added to a lab.
var (
	// ErrToolsNoImage: the platform has no tools image configured.
	ErrToolsNoImage = errors.New("the platform tools image is not configured")
	// ErrToolsNoVPN: the lab has no VPN network (or no DHCP on it), so the tools device would have no place among the
	// participants' machines.
	ErrToolsNoVPN = errors.New("the lab has no VPN network with DHCP for the tools device")
)

// ToolsDeviceName is the logical name of the platform tools device in a lab; a taken name gets a number.
const ToolsDeviceName = "tools"

// toolsSwitchName is the display name of the switch put between the VPN gateway and the device it was cabled to.
const toolsSwitchName = "tools-switch"

// WithToolsDevice returns a copy of the topology with the platform tools device added: a container of the given image that
// sits on the segment of the VPN gateway (where the participants' machines are) and is reached out of band through its grpc
// access. It has no connection to the internet. If the gateway is cabled to a switch or hub the device takes a free port of
// it; if it is cabled to a device, an unmanaged switch is put between them (the same L2 segment); if it is not cabled at all
// the device is cabled to it directly. The copy is validated; the input is never changed.
func (t Topology) WithToolsDevice(image string) (Topology, Device, error) {
	if image == "" {
		return Topology{}, Device{}, ErrToolsNoImage
	}
	vpn, ok := t.vpnGatewayWithDHCP()
	if !ok {
		return Topology{}, Device{}, ErrToolsNoVPN
	}
	out := t
	out.Devices = append([]Device(nil), t.Devices...)
	out.Connections = make([]Connection, len(t.Connections))
	for i, c := range t.Connections {
		out.Connections[i] = Connection{Endpoints: append([]Endpoint(nil), c.Endpoints...)}
	}

	names := make(map[string]bool, len(t.Devices))
	for _, d := range t.Devices {
		names[d.Name] = true
	}
	name := ToolsDeviceName
	for n := 2; names[name]; n++ {
		name = ToolsDeviceName + "-" + strconv.Itoa(n)
	}
	tools := Device{
		ID: uuid.Must(uuid.NewV7()), Name: name, Type: DeviceTypeNode, Profile: NodeProfileContainer, Image: image,
		// The extended preset: the tools scan and probe, which needs raw sockets.
		SecurityPreset: SecurityPresetExtended,
		Interfaces:     []Interface{{Name: "eth0", IP: IPConfig{Type: IPConfigTypeDHCPPreset}}},
		Access:         []Access{{Kind: AccessGRPC}},
	}
	toolsEnd := Endpoint{Kind: EndpointDevice, DeviceID: tools.ID, Interface: "eth0"}

	vpnAt := -1
	for i, c := range out.Connections {
		for _, ep := range c.Endpoints {
			if ep.Kind == EndpointDevice && ep.DeviceID == vpn.ID {
				vpnAt = i
			}
		}
	}
	gatewayEnd := Endpoint{Kind: EndpointDevice, DeviceID: vpn.ID, Interface: GatewayPort}
	switch {
	case vpnAt < 0:
		out.Connections = append(out.Connections, Connection{Endpoints: []Endpoint{gatewayEnd, toolsEnd}})
	default:
		other := otherEnd(out.Connections[vpnAt], vpn.ID)
		var peer Device
		for _, d := range out.Devices {
			if d.ID == other.DeviceID {
				peer = d
			}
		}
		if peer.Type.IsForwarding() {
			port, ok := freePort(out.Connections, peer.ID)
			if !ok {
				return Topology{}, Device{}, fmt.Errorf("the switch of the VPN gateway has no free port")
			}
			out.Connections = append(out.Connections, Connection{Endpoints: []Endpoint{toolsEnd, {Kind: EndpointDevice, DeviceID: peer.ID, Interface: port}}})
		} else {
			sw := Device{ID: uuid.Must(uuid.NewV7()), Name: toolsSwitchName, Type: DeviceTypeUnmanagedSwitch}
			out.Devices = append(out.Devices, sw)
			port := func(n int) Endpoint {
				return Endpoint{Kind: EndpointDevice, DeviceID: sw.ID, Interface: "GigabitEthernet0/" + strconv.Itoa(n)}
			}
			out.Connections[vpnAt] = Connection{Endpoints: []Endpoint{gatewayEnd, port(1)}}
			out.Connections = append(out.Connections,
				Connection{Endpoints: []Endpoint{other, port(2)}},
				Connection{Endpoints: []Endpoint{toolsEnd, port(3)}})
		}
	}
	out.Devices = append(out.Devices, tools)

	if err := out.validateStructure(); err != nil {
		return Topology{}, Device{}, err
	}
	if err := out.validateGraph(); err != nil {
		return Topology{}, Device{}, err
	}
	return out, tools, nil
}

// otherEnd is the end of a connection that is not the device.
func otherEnd(c Connection, device uuid.UUID) Endpoint {
	for _, ep := range c.Endpoints {
		if ep.DeviceID != device {
			return ep
		}
	}
	return Endpoint{}
}

// vpnGatewayWithDHCP is the first VPN gateway of the topology whose DHCP server is on: where the tools device gets its address.
func (t Topology) vpnGatewayWithDHCP() (Device, bool) {
	for _, d := range t.Devices {
		if d.Type == DeviceTypeVPNGateway && d.DHCP {
			return d, true
		}
	}
	return Device{}, false
}

// freePort is the first port of a switch or hub no connection uses.
func freePort(conns []Connection, id uuid.UUID) (string, bool) {
	used := map[string]bool{}
	for _, c := range conns {
		for _, ep := range c.Endpoints {
			if ep.Kind == EndpointDevice && ep.DeviceID == id {
				used[ep.Interface] = true
			}
		}
	}
	for n := 1; n <= 48; n++ {
		if p := "GigabitEthernet0/" + strconv.Itoa(n); !used[p] {
			return p, true
		}
	}
	return "", false
}
