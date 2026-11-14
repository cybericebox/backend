package exerciseModel

import (
	"errors"
	"slices"
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
	// ErrToolsNoSegment: the segment the device is to sit on (the foothold, or the segment of an operated device) is not in the
	// lab, or has no free port.
	ErrToolsNoSegment = errors.New("the lab has no such segment for the tools device")
)

// ToolsDeviceName is the logical name of the platform tools device in a lab; a taken name gets a number.
const ToolsDeviceName = "tools"

// toolsSwitchName is the display name of the switch put between the VPN gateway and the device it was cabled to.
const toolsSwitchName = "tools-switch"

// ToolsPlacement says where the tools device sits and what it is told. The zero value is the segment of the VPN gateway, where
// the participants' machines are.
type ToolsPlacement struct {
	// Foothold is the name of a switch or hub of the topology: the device takes a port of it (the attacker's place).
	Foothold string
	// Beside are names of devices: the device sits on the segment of the first of them (the defender's place, with the devices
	// it operates). Ignored when Foothold is set.
	Beside []string
	// Env is what the device's service process is given (see EDAEnv).
	Env []EnvVar
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

// The values of Device.Internet.
const (
	InternetAllow = "allow"
	InternetDeny  = "deny"
)

// Names of the values a tools device is given to check the credentials of its callers.
const (
	// EnvEDATrustCA is the brain's CA (PEM): the runner verifies the brain's own credential against it.
	EnvEDATrustCA = "EDA_TRUST_CA"
	// EnvEDARunID is the run the device works for: the runner checks the audience of its token against it.
	EnvEDARunID = "EDA_RUN_ID"
)

// EDAEnv is the environment of a tools device for one run. It is the one place that says how the values reach the device
// (today: the device's bootstrap env).
func EDAEnv(runID, trustCA string) []EnvVar {
	env := []EnvVar{{Name: EnvEDARunID, Value: runID}}
	if trustCA != "" {
		env = append(env, EnvVar{Name: EnvEDATrustCA, Value: trustCA})
	}
	return env
}

// WithToolsDevice returns a copy of the topology with the platform tools device added: a container of the given image that is
// reached over the lab network by the VPN client of its run. It has no connection to the internet. Where it sits is the placement: a port
// of the foothold switch, the segment of an operated device, or (by default) the segment of the VPN gateway. On a segment
// that is a switch or hub the device takes a free port; where the anchor is cabled to a device an unmanaged switch is put
// between them (the same L2 segment). The copy is validated; the input is never changed.
func (t Topology) WithToolsDevice(image string, place ToolsPlacement) (Topology, Device, error) {
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

	byName := make(map[string]Device, len(t.Devices))
	for _, d := range t.Devices {
		byName[d.Name] = d
	}
	name := ToolsDeviceName
	for n := 2; byName[name].ID != uuid.Nil; n++ {
		name = ToolsDeviceName + "-" + strconv.Itoa(n)
	}
	tools := Device{
		ID: uuid.Must(uuid.NewV7()), Name: name, Type: DeviceTypeNode, Profile: NodeProfileContainer, Image: image,
		// The extended preset: the tools scan and probe, which needs raw sockets.
		SecurityPreset: SecurityPresetExtended,
		Interfaces:     []Interface{{Name: "eth0", IP: IPConfig{Type: IPConfigTypeDHCPPreset}}},
		// No internet link exists for it, and it is denied explicitly as well (defense in depth: the laboratory then refuses a lab
		// in which it would share a broadcast domain with an Internet gateway).
		Internet:  InternetDeny,
		Access:    []Access{{Kind: AccessGRPC}},
		Bootstrap: Bootstrap{Env: slices.Clone(place.Env)},
	}
	toolsEnd := Endpoint{Kind: EndpointDevice, DeviceID: tools.ID, Interface: "eth0"}

	switch {
	case place.Foothold != "":
		sw, ok := byName[place.Foothold]
		if !ok || !sw.Type.IsForwarding() {
			return Topology{}, Device{}, ErrToolsNoSegment
		}
		port, ok := freePort(out.Connections, sw.ID)
		if !ok {
			return Topology{}, Device{}, ErrToolsNoSegment
		}
		out.Connections = append(out.Connections, Connection{Endpoints: []Endpoint{toolsEnd, {Kind: EndpointDevice, DeviceID: sw.ID, Interface: port}}})
	case len(place.Beside) > 0:
		anchor, ok := byName[place.Beside[0]]
		if !ok {
			return Topology{}, Device{}, ErrToolsNoSegment
		}
		at := -1
		for i, c := range out.Connections {
			for _, ep := range c.Endpoints {
				if ep.Kind == EndpointDevice && ep.DeviceID == anchor.ID {
					at = i
				}
			}
			if at >= 0 {
				break
			}
		}
		if at < 0 {
			return Topology{}, Device{}, ErrToolsNoSegment
		}
		if err := out.cableBeside(at, Endpoint{Kind: EndpointDevice, DeviceID: anchor.ID}, toolsEnd); err != nil {
			return Topology{}, Device{}, err
		}
	default:
		// The segment of the VPN gateway, where the participants' machines are.
		at := -1
		for i, c := range out.Connections {
			for _, ep := range c.Endpoints {
				if ep.Kind == EndpointDevice && ep.DeviceID == vpn.ID {
					at = i
				}
			}
			if at >= 0 {
				break
			}
		}
		if at < 0 {
			out.Connections = append(out.Connections, Connection{Endpoints: []Endpoint{{Kind: EndpointDevice, DeviceID: vpn.ID, Interface: GatewayPort}, toolsEnd}})
		} else if err := out.cableBeside(at, Endpoint{Kind: EndpointDevice, DeviceID: vpn.ID}, toolsEnd); err != nil {
			return Topology{}, Device{}, err
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

// cableBeside puts the tools end on the segment of the connection at: the one the anchor end is on. The other end of the
// connection is the segment's peer: a switch or hub gets the tools device on a free port, a device gets an unmanaged
// switch between itself and the anchor.
func (t *Topology) cableBeside(at int, anchor, toolsEnd Endpoint) error {
	conn := t.Connections[at]
	var anchorEnd, other Endpoint
	for _, ep := range conn.Endpoints {
		if ep.Kind == anchor.Kind && ep.DeviceID == anchor.DeviceID {
			anchorEnd = ep
		} else {
			other = ep
		}
	}
	var peer Device
	for _, d := range t.Devices {
		if other.Kind == EndpointDevice && d.ID == other.DeviceID {
			peer = d
		}
	}
	if peer.Type.IsForwarding() {
		port, ok := freePort(t.Connections, peer.ID)
		if !ok {
			return ErrToolsNoSegment
		}
		t.Connections = append(t.Connections, Connection{Endpoints: []Endpoint{toolsEnd, {Kind: EndpointDevice, DeviceID: peer.ID, Interface: port}}})
		return nil
	}
	sw := Device{ID: uuid.Must(uuid.NewV7()), Name: toolsSwitchName, Type: DeviceTypeUnmanagedSwitch}
	t.Devices = append(t.Devices, sw)
	port := func(n int) Endpoint {
		return Endpoint{Kind: EndpointDevice, DeviceID: sw.ID, Interface: "GigabitEthernet0/" + strconv.Itoa(n)}
	}
	t.Connections[at] = Connection{Endpoints: []Endpoint{anchorEnd, port(1)}}
	t.Connections = append(t.Connections, Connection{Endpoints: []Endpoint{other, port(2)}}, Connection{Endpoints: []Endpoint{toolsEnd, port(3)}})
	return nil
}

func otherEnd(c Connection, kind EndpointKind) Endpoint {
	for _, ep := range c.Endpoints {
		if ep.Kind != kind {
			return ep
		}
	}
	return Endpoint{}
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
