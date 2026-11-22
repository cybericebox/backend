package exerciseModel

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/gofrs/uuid"
)

// Why the platform tools device cannot be added to a lab.
var (
	// ErrToolsNoImage: the platform has no tools image configured.
	ErrToolsNoImage = errors.New("the platform tools image is not configured")
	// ErrToolsNoVPN: the lab has no VPN network (or no DHCP on it), so the tools device would have no place among the
	// participants' machines.
	ErrToolsNoVPN = errors.New("the lab has no VPN network with DHCP for the tools device")
	// ErrToolsNoControl: the lab has no control gateway with a /24 yet (or it is not a lab with a tools device), or the topology
	// has a device named like one of the control gateways already (a task's own, or a control added by an earlier call).
	ErrToolsNoControl = errors.New("the lab has no control address for the tools device")
	// ErrToolsNoSegment: the segment the device is to sit on (the foothold, or the segment of an operated device) is not in the
	// lab, or has no free port.
	ErrToolsNoSegment = errors.New("the lab has no such segment for the tools device")
	// ErrToolsTooLarge: the size the tools device was asked for does not fit the device limits of a lab at a workable size.
	ErrToolsTooLarge = errors.New("the tools device does not fit the device limits of the lab")
	// ErrToolsTooMany: more tools devices were asked for than a lab may hold (MaxOpponents), or their control gateways would take
	// the lab over the gateways it may have (MaxLabGateways).
	ErrToolsTooMany = errors.New("the lab cannot hold that many tools devices")
	// ErrToolsPullConflict: the lab pulls other images from the registry of the tools image with another credential.
	ErrToolsPullConflict = errors.New("the tools image registry is already used by the lab with another credential")
)

// ToolsDeviceName is the logical name of the platform tools device in a lab; a taken name gets a number.
const ToolsDeviceName = "tools"

// toolsSwitchName is the display name of the switch put between an anchor (the VPN gateway, an operated device) and the device it
// was cabled to; a taken name gets a number.
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
	// Image is the image of the device as the brain specified it (GetOpponent), applied as it is; empty: the platform's
	// configured tools image (the deprecated BRAIN_TOOLS_IMAGE fallback), checked like every other image.
	Image string
	// CPUMillis and MemoryBytes are the size the device is asked for (hints: clamped to the device limits of a lab); zero: the
	// platform's default device size.
	CPUMillis, MemoryBytes int64
	// ResourcePreset is the preset the device gets (set by the deploy from the hints).
	ResourcePreset string
	// Pull is the short-lived credential Image is pulled with: handed to the laboratory as a pull secret of this lab only,
	// never stored or logged. nil: the image is public or pullable already.
	Pull *ToolsPull
}

// ToolsPull is a registry credential for the tools image. Its String and GoString never show the password.
type ToolsPull struct {
	Registry, Username, Password string
}

func (p ToolsPull) String() string {
	return "{Registry:" + p.Registry + " Username:" + p.Username + " Password:[REDACTED]}"
}

// GoString is String: a %#v must not print the password either.
func (p ToolsPull) GoString() string { return p.String() }

// The values of Device.Internet.
const (
	InternetAllow = "allow"
	InternetDeny  = "deny"
)

// The control gateways: VPN gateways of their own that carry only the platform's control of the tools devices. Every tools
// device has its own: its own gateway with its own /24 and its own switch, on which there is nothing but that gateway and the
// device's second interface. A tools device has raw sockets and NET_ADMIN, so on a shared control segment it could take the
// address of another device (and answer the brain in its place); with a control L2 domain each, no device can reach the control
// of another. The brain's run client reaches the device's second interface there, at one fixed address, and nowhere else.
const (
	// ControlGatewayName is the name of the control gateway of the first tools device (index 0) in the lab spec: a lab of one
	// opponent is the lab it always was. The next devices have ControlGatewayOf(index).
	ControlGatewayName = "ctl"
	// ControlHostBase is the host of a tools device in the /24 of its own control gateway: the same on every gateway, so the
	// index of the device selects the gateway, never the host.
	ControlHostBase = 10
	// RunnerPort is the port the runner of a tools device listens on.
	RunnerPort        = 9099
	controlSwitchName = "ctl-switch"
)

// ControlGatewayOf is the name of the control gateway of the tools device of an index: "ctl" for the first, then "ctl-2",
// "ctl-3", ... (the number counts the devices from one, as the names of the tools devices themselves do: tools, tools-2, ...).
func ControlGatewayOf(index int) string {
	if index <= 0 {
		return ControlGatewayName
	}
	return ControlGatewayName + "-" + strconv.Itoa(index+1)
}

// IsControlGatewayName says the name is one the platform gives a control gateway (any index up to MaxOpponents), compared as the
// validation compares device names: trimmed, any case. Such a name is reserved for the platform.
func IsControlGatewayName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == ControlGatewayName {
		return true
	}
	rest, ok := strings.CutPrefix(name, ControlGatewayName+"-")
	if !ok || rest == "" || rest[0] == '0' {
		return false
	}
	n, err := strconv.Atoi(rest)
	return err == nil && n >= 2 && n <= MaxOpponents
}

// control is the control gateway of one tools device and its switch.
type control struct {
	gateway  string
	switchID uuid.UUID
}

// addControl gives the topology the control gateway of the tools device of an index: gateway - switch, the gateway on the first
// port. The gateway is an ordinary VPN gateway device of the copy (no DHCP, no reverse connections); only the platform adds it,
// at deploy time, for its one device (WithToolsDevices). A device named like the gateway is refused: it is a task's own (the
// name is reserved at validation as well) or a control added before, and the gateway would not be the platform's.
func (t *Topology) addControl(index int) (control, error) {
	name := ControlGatewayOf(index)
	for _, d := range t.Devices {
		if strings.EqualFold(strings.TrimSpace(d.Name), name) {
			return control{}, ErrToolsNoControl
		}
	}
	gateway := Device{ID: uuid.Must(uuid.NewV7()), Name: name, Type: DeviceTypeVPNGateway}
	sw := Device{ID: uuid.Must(uuid.NewV7()), Name: t.freeName(controlSwitchName), Type: DeviceTypeUnmanagedSwitch}
	t.Devices = append(t.Devices, gateway, sw)
	t.Connections = append(t.Connections, Connection{Endpoints: []Endpoint{{Kind: EndpointDevice, DeviceID: gateway.ID, Interface: GatewayPort}, switchPort(sw.ID, 1)}})
	return control{gateway: name, switchID: sw.ID}, nil
}

// attach gives the tools device its second interface on its control: eth1 at ControlHostBase of the gateway's /24, on the
// second port of the switch (the gateway has the first one). The first interface is left where its placement put it.
func (c control) attach(t *Topology, tools *Device) {
	tools.Interfaces = append(tools.Interfaces, Interface{Name: "eth1", IP: IPConfig{
		Type: IPConfigTypeStatic, AddressRef: &NetworkIPRef{Gateway: c.gateway, Host: ControlHostBase},
	}})
	t.Connections = append(t.Connections, Connection{Endpoints: []Endpoint{{Kind: EndpointDevice, DeviceID: tools.ID, Interface: "eth1"}, switchPort(c.switchID, 2)}})
}

func switchPort(id uuid.UUID, n int) Endpoint {
	return Endpoint{Kind: EndpointDevice, DeviceID: id, Interface: "GigabitEthernet0/" + strconv.Itoa(n)}
}

// freeName is base, or base-2, base-3, ... when a device has that name already.
func (t Topology) freeName(base string) string {
	taken := make(map[string]bool, len(t.Devices))
	for _, d := range t.Devices {
		taken[d.Name] = true
	}
	name := base
	for n := 2; taken[name]; n++ {
		name = base + "-" + strconv.Itoa(n)
	}
	return name
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

// WithToolsDevice is WithToolsDevices with one device of the given image (index 0).
func (t Topology) WithToolsDevice(image string, place ToolsPlacement) (Topology, Device, error) {
	place.Image = image
	out, tools, err := t.WithToolsDevices([]ToolsPlacement{place})
	if err != nil {
		return Topology{}, Device{}, err
	}
	return out, tools[0], nil
}

// WithToolsDevices returns a copy of the topology with the platform tools devices added, one per placement and in its order:
// each a container of the placement's image that is reached over the lab network by the VPN client of its run. None has a
// connection to the internet. Where one sits is its placement: a port of the foothold switch, the segment of an operated device,
// or (by default) the segment of the VPN gateway. On a segment that is a switch or hub the device takes a free port; where the
// anchor is cabled to a device an unmanaged switch is put between them (the same L2 segment), and a later device on that segment
// takes a port of that switch: the data side of the devices is shared by design (they play against the same segments). The
// control side is not: the device at position i (its index) gets a control gateway and switch of its own, ControlGatewayOf(i),
// and the address ControlHostBase there. At most MaxOpponents devices are added, and only while the gateways of the task and
// the control gateways stay within MaxLabGateways; every device of a lab is added in this one call (a topology that has a
// control gateway already is refused). The copy is validated; the input is never changed.
func (t Topology) WithToolsDevices(places []ToolsPlacement) (Topology, []Device, error) {
	if len(places) > MaxOpponents {
		return Topology{}, nil, ErrToolsTooMany
	}
	for _, place := range places {
		if place.Image == "" {
			return Topology{}, nil, ErrToolsNoImage
		}
	}
	if len(places) == 0 {
		return t, nil, nil
	}
	vpn, ok := t.vpnGatewayWithDHCP()
	if !ok {
		return Topology{}, nil, ErrToolsNoVPN
	}
	gateways := 0
	for _, d := range t.Devices {
		if d.Type.IsGateway() {
			gateways++
		}
	}
	if gateways+len(places) > MaxLabGateways {
		return Topology{}, nil, ErrToolsTooMany
	}
	out := t
	out.Devices = append([]Device(nil), t.Devices...)
	out.Connections = make([]Connection, len(t.Connections))
	for i, c := range t.Connections {
		out.Connections[i] = Connection{Endpoints: append([]Endpoint(nil), c.Endpoints...)}
	}

	devices := make([]Device, 0, len(places))
	for index, place := range places {
		tools, err := out.placeTools(vpn, place)
		if err != nil {
			return Topology{}, nil, err
		}
		ctl, err := out.addControl(index)
		if err != nil {
			return Topology{}, nil, err
		}
		ctl.attach(&out, &tools)
		out.Devices = append(out.Devices, tools)
		devices = append(devices, tools)
	}

	if err := out.validateStructure(); err != nil {
		return Topology{}, nil, err
	}
	if err := out.validateGraph(); err != nil {
		return Topology{}, nil, err
	}
	return out, devices, nil
}

// placeTools makes the tools device of one placement and cables its first interface where the placement says (the device is
// not in the topology yet: the caller appends it).
func (t *Topology) placeTools(vpn Device, place ToolsPlacement) (Device, error) {
	byName := make(map[string]Device, len(t.Devices))
	for _, d := range t.Devices {
		byName[d.Name] = d
	}
	tools := Device{
		ID: uuid.Must(uuid.NewV7()), Name: t.freeName(ToolsDeviceName), Type: DeviceTypeNode, Profile: NodeProfileContainer, Image: place.Image, ResourcePreset: place.ResourcePreset,
		// The extended preset: the tools scan and probe, which needs raw sockets.
		SecurityPreset: SecurityPresetExtended,
		Interfaces:     []Interface{{Name: "eth0", IP: IPConfig{Type: IPConfigTypeDHCPPreset}}},
		// No internet link exists for it, and it is denied explicitly as well (defense in depth: the laboratory then refuses a lab
		// in which it would share a broadcast domain with an Internet gateway).
		Internet:  InternetDeny,
		Bootstrap: Bootstrap{Env: slices.Clone(place.Env)},
	}
	toolsEnd := Endpoint{Kind: EndpointDevice, DeviceID: tools.ID, Interface: "eth0"}

	switch {
	case place.Foothold != "":
		sw, ok := byName[place.Foothold]
		if !ok || !sw.Type.IsForwarding() {
			return Device{}, ErrToolsNoSegment
		}
		port, ok := freePort(t.Connections, sw.ID)
		if !ok {
			return Device{}, ErrToolsNoSegment
		}
		t.Connections = append(t.Connections, Connection{Endpoints: []Endpoint{toolsEnd, {Kind: EndpointDevice, DeviceID: sw.ID, Interface: port}}})
	case len(place.Beside) > 0:
		anchor, ok := byName[place.Beside[0]]
		if !ok {
			return Device{}, ErrToolsNoSegment
		}
		at := t.firstConnectionOf(anchor.ID)
		if at < 0 {
			return Device{}, ErrToolsNoSegment
		}
		if err := t.cableBeside(at, Endpoint{Kind: EndpointDevice, DeviceID: anchor.ID}, toolsEnd); err != nil {
			return Device{}, err
		}
	default:
		// The segment of the VPN gateway, where the participants' machines are.
		if at := t.firstConnectionOf(vpn.ID); at < 0 {
			t.Connections = append(t.Connections, Connection{Endpoints: []Endpoint{{Kind: EndpointDevice, DeviceID: vpn.ID, Interface: GatewayPort}, toolsEnd}})
		} else if err := t.cableBeside(at, Endpoint{Kind: EndpointDevice, DeviceID: vpn.ID}, toolsEnd); err != nil {
			return Device{}, err
		}
	}
	return tools, nil
}

// firstConnectionOf is the index of the first connection with an end at the device, -1 when it has none.
func (t Topology) firstConnectionOf(id uuid.UUID) int {
	for i, c := range t.Connections {
		for _, ep := range c.Endpoints {
			if ep.Kind == EndpointDevice && ep.DeviceID == id {
				return i
			}
		}
	}
	return -1
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
	// A later tools device on this segment finds the switch as the anchor's peer and takes a free port of it.
	sw := Device{ID: uuid.Must(uuid.NewV7()), Name: t.freeName(toolsSwitchName), Type: DeviceTypeUnmanagedSwitch}
	t.Devices = append(t.Devices, sw)
	t.Connections[at] = Connection{Endpoints: []Endpoint{anchorEnd, switchPort(sw.ID, 1)}}
	t.Connections = append(t.Connections, Connection{Endpoints: []Endpoint{other, switchPort(sw.ID, 2)}}, Connection{Endpoints: []Endpoint{toolsEnd, switchPort(sw.ID, 3)}})
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
