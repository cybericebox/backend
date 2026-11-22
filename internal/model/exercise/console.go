package exerciseModel

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/gofrs/uuid"
)

// ConnectionKind tells a network connection (empty: every connection saved before console cables) from a console cable.
type ConnectionKind string

const (
	ConnectionNetwork ConnectionKind = ""
	// ConnectionConsole is a console cable: one serial port end and one console port end, outside the lab networks.
	ConnectionConsole ConnectionKind = "console"
)

func (k ConnectionKind) Valid() bool { return k == ConnectionNetwork || k == ConnectionConsole }

// NativeKind is where an emulator or a VM already puts its console.
type NativeKind string

const (
	// NativeStdio: the console is the service's own stdin, stdout and stderr.
	NativeStdio NativeKind = "stdio"
	// NativeTCP: the console is a TCP port on the device's own loopback.
	NativeTCP NativeKind = "tcp"
	// NativeVMSerial is the serial device of a full VM. The platform sets it for the vm profile; an author never writes it.
	NativeVMSerial NativeKind = "vm-serial"
)

type (
	// ConsolePort is a management line of a device. At most one of Command and Native; neither runs DefaultConsoleCommand
	// as DefaultConsoleUser (the laboratory fills the default).
	ConsolePort struct {
		Name    string        `json:"name"`              // console0..console99, aux0..aux99
		Command []string      `json:"command,omitempty"` // empty: DefaultConsoleCommand
		User    string        `json:"user,omitempty"`    // empty: DefaultConsoleUser
		Native  *NativeSource `json:"native,omitempty"`
	}

	// NativeSource is where an emulator or a VM already puts its console.
	NativeSource struct {
		Kind NativeKind `json:"kind"`
		Port int32      `json:"port,omitempty"` // tcp only: a port on the device's own loopback
	}

	// SerialPort is a terminal line of a connector device (a PC, a laptop, a tools device): a character device inside it.
	SerialPort struct {
		Name string `json:"name"`           // serial0..serial99
		Path string `json:"path,omitempty"` // empty: SerialPathPrefix + the number of the name
	}
)

// Limits of the console lines. The laboratory owns them (names.MaxConsolePorts, names.MaxSerialPorts and the cable ceiling,
// reported with its device constants); keep them in sync.
const (
	MaxConsolePorts        = 4
	MaxSerialPorts         = 16
	MaxConsoleCablesPerLab = 64
)

const (
	// DefaultConsoleCommand and DefaultConsoleUser run on a console port that declares neither a command nor a native line.
	DefaultConsoleCommand = "/bin/login"
	DefaultConsoleUser    = "root"
	// SerialPathPrefix is the default character device of a serial port: serial<n> is SerialPathPrefix<n>.
	SerialPathPrefix = "/dev/ttyCON"
)

var (
	consolePortNameRe = regexp.MustCompile(`^(console|aux)(0|[1-9][0-9]?)$`)
	serialPortNameRe  = regexp.MustCompile(`^serial(0|[1-9][0-9]?)$`)
	serialPathRe      = regexp.MustCompile(`^/dev/tty[A-Z][A-Za-z0-9]{0,14}$`)
)

// PortKind is what a name of a device addresses as a connection end.
type PortKind string

const (
	PortNone    PortKind = ""
	PortNIC     PortKind = "nic" // a network interface, a switch or hub port, the port of a gateway
	PortSerial  PortKind = "serial"
	PortConsole PortKind = "console"
)

// PortOf is the kind of the port of the device with that name. Port names and interface names share one space per device,
// so the answer is never ambiguous on a valid device.
func (d Device) PortOf(name string) PortKind {
	switch {
	case d.Type.IsForwarding():
		if validForwardingPort(name) {
			return PortNIC
		}
		return PortNone
	case d.Type.IsGateway():
		if name == GatewayPort {
			return PortNIC
		}
		return PortNone
	case hasInterface(d, name):
		return PortNIC
	}
	for _, p := range d.ConsolePorts {
		if p.Name == name {
			return PortConsole
		}
	}
	for _, p := range d.SerialPorts {
		if p.Name == name {
			return PortSerial
		}
	}
	return PortNone
}

// IsConsole reports whether the connection is a console cable.
func (c Connection) IsConsole() bool { return c.Kind == ConnectionConsole }

// UsesConsoleLines reports whether the topology declares a console or serial port or a console cable: the topology keys of
// the document schema 1.1. (UsesConsole, of variants, is the browser terminal: another thing.)
func (t Topology) UsesConsoleLines() bool {
	for _, d := range t.Devices {
		if len(d.ConsolePorts) > 0 || len(d.SerialPorts) > 0 {
			return true
		}
	}
	for _, c := range t.Connections {
		if c.IsConsole() {
			return true
		}
	}
	return false
}

// DevicePath is the character device of the serial port inside its device.
func (p SerialPort) DevicePath() string {
	if p.Path != "" {
		return p.Path
	}
	return SerialPathPrefix + strings.TrimPrefix(p.Name, "serial")
}

// validateDevicePorts checks the console and serial ports of one device on their own (rules 1-6 of the console design):
// ports only on a node that is not Windows, names of their pattern and unique among the device's ports and interfaces, the
// limits, the console program or native line, the serial device path.
func validateDevicePorts(d Device) error {
	if len(d.ConsolePorts) == 0 && len(d.SerialPorts) == 0 {
		return nil
	}
	if !d.Type.IsNode() || d.System == SystemWindows {
		field := "console_ports"
		if len(d.ConsolePorts) == 0 {
			field = "serial_ports"
		}
		reason := "device_type"
		if d.Type.IsNode() {
			reason = "system"
		}
		return ErrConsolePortNotAllowed.WithPublicContext("device", d.Name).
			WithPublicContext("field", field).
			WithPublicContext("reason", reason).
			Err()
	}
	if kind, field, limit := portLimitExceeded(d); kind != "" {
		return ErrDevicePortLimit.WithPublicContext("device", d.Name).
			WithPublicContext("field", field).
			WithPublicContext("kind", kind).
			WithPublicContext("limit", limit).
			Err()
	}
	taken := make(map[string]bool, len(d.Interfaces)+len(d.ConsolePorts)+len(d.SerialPorts))
	for _, iface := range d.Interfaces {
		taken[iface.Name] = true
	}
	stdio := false
	for i, p := range d.ConsolePorts {
		field := fmt.Sprintf("console_ports[%d]", i+1)
		if !consolePortNameRe.MatchString(p.Name) {
			return consolePortInvalid(d, field+".name", "name")
		}
		if taken[p.Name] {
			return portNameTaken(d, field+".name", p.Name)
		}
		taken[p.Name] = true
		if key, reason := consolePortProgramInvalid(p); reason != "" {
			return consolePortInvalid(d, field+"."+key, reason)
		}
		if p.Native != nil && p.Native.Kind == NativeStdio {
			if stdio {
				return consolePortInvalid(d, field+".native", "stdio_twice")
			}
			stdio = true
		}
	}
	paths := make(map[string]bool, len(d.SerialPorts))
	for i, p := range d.SerialPorts {
		field := fmt.Sprintf("serial_ports[%d]", i+1)
		if !serialPortNameRe.MatchString(p.Name) {
			return serialPortInvalid(d, field+".name", "name")
		}
		if taken[p.Name] {
			return portNameTaken(d, field+".name", p.Name)
		}
		taken[p.Name] = true
		if p.Path != "" && !validSerialPath(p.Path) || paths[p.DevicePath()] {
			return serialPortInvalid(d, field+".path", "path")
		}
		paths[p.DevicePath()] = true
	}
	return nil
}

// portLimitExceeded names the kind of port of which the device has too many, with its field and limit; "" when none.
func portLimitExceeded(d Device) (kind, field string, limit int) {
	if len(d.ConsolePorts) > MaxConsolePorts {
		return "console", "console_ports", MaxConsolePorts
	}
	if len(d.SerialPorts) > MaxSerialPorts {
		return "serial", "serial_ports", MaxSerialPorts
	}
	return "", "", 0
}

// consolePortProgramInvalid names the key of a console port whose program is wrong and why: exactly one of a command and a
// native line (neither is the default command), the command and user of the terminal rules, a native kind an author may
// write with its port.
func consolePortProgramInvalid(p ConsolePort) (key, reason string) {
	if p.Native != nil {
		switch {
		case len(p.Command) > 0:
			return "native", "native"
		case p.User != "":
			return "user", "user" // a native line runs no program of the platform, so it has no user
		case p.Native.Kind == NativeStdio && p.Native.Port != 0,
			p.Native.Kind == NativeTCP && (p.Native.Port < 1 || p.Native.Port > 65535),
			p.Native.Kind != NativeStdio && p.Native.Kind != NativeTCP:
			return "native", "native"
		}
		return "", ""
	}
	if !validConsoleCommand(p.Command) {
		return "command", "command"
	}
	if p.User != "" && !accountNameRe.MatchString(p.User) {
		return "user", "user"
	}
	return "", ""
}

// validConsoleCommand: the rules of the terminal command of the laboratory. At most MaxCommandEntries entries of at most
// MaxCommandEntryBytes without NUL; the program is an absolute path outside /proc and /sys. Empty is the default.
func validConsoleCommand(command []string) bool {
	if len(command) == 0 {
		return true
	}
	if len(command) > MaxCommandEntries {
		return false
	}
	for _, entry := range command {
		if len(entry) > MaxCommandEntryBytes || strings.ContainsRune(entry, 0) {
			return false
		}
	}
	program := command[0]
	if !strings.HasPrefix(program, "/") {
		return false
	}
	clean := path.Clean(program)
	for _, virtual := range []string{"/proc", "/sys"} {
		if clean == virtual || strings.HasPrefix(clean, virtual+"/") {
			return false
		}
	}
	return true
}

func validSerialPath(p string) bool {
	switch p {
	case "/dev/tty", "/dev/console", "/dev/ptmx":
		return false
	}
	return serialPathRe.MatchString(p)
}

func consolePortInvalid(d Device, field, reason string) error {
	return ErrConsolePortInvalid.WithPublicContext("device", d.Name).
		WithPublicContext("field", field).
		WithPublicContext("reason", reason).
		Err()
}

func serialPortInvalid(d Device, field, reason string) error {
	return ErrSerialPortInvalid.WithPublicContext("device", d.Name).
		WithPublicContext("field", field).
		WithPublicContext("reason", reason).
		Err()
}

func portNameTaken(d Device, field, port string) error {
	return ErrDevicePortNameTaken.WithPublicContext("device", d.Name).
		WithPublicContext("field", field).
		WithPublicContext("port", port).
		Err()
}

// ValidateConsoleLines is the cheap guard of every save, the working copy included (which skips the business validation): a
// connection kind is empty or console, and a network connection never ends on a console or serial port of a node, nor on a
// name of a port (console0, aux0, serial0, ...) that is not an interface of the node. The second rule keeps a console cable
// from becoming a network connection silently when a client drops the ports and the kind but keeps the ends. The variant is
// named on the error.
func ValidateConsoleLines(variants []Variant) error {
	for vi, variant := range variants {
		if err := variant.Topology.validateConsoleLines(); err != nil {
			return InVariant(err, vi+1)
		}
	}
	return nil
}

func (t Topology) validateConsoleLines() error {
	byID := make(map[uuid.UUID]Device, len(t.Devices))
	for _, d := range t.Devices {
		byID[d.ID] = d
	}
	for ci, c := range t.Connections {
		if err := validateConnectionKind(c, ci+1); err != nil {
			return err
		}
		if c.IsConsole() {
			continue
		}
		for _, ep := range c.Endpoints {
			d, ok := byID[ep.DeviceID]
			if !ok || !d.Type.IsNode() || hasInterface(d, ep.Interface) {
				continue // resolved, or refused, by the graph rules
			}
			kind := d.PortOf(ep.Interface)
			if kind == PortSerial || kind == PortConsole ||
				consolePortNameRe.MatchString(ep.Interface) || serialPortNameRe.MatchString(ep.Interface) {
				return ErrConsolePortOnNetwork.WithPublicContext("connection", ci+1).
					WithPublicContext("device", d.Name).
					WithPublicContext("port", ep.Interface).
					WithPublicContext("field", "kind").
					Err()
			}
		}
	}
	return nil
}

// validateConnectionKind is the shape rule of a connection kind (rule 7); connection is 1-based.
func validateConnectionKind(c Connection, connection int) error {
	if c.Kind.Valid() {
		return nil
	}
	return ErrConnectionKindInvalid.WithPublicContext("connection", connection).
		WithPublicContext("field", "kind").
		Err()
}

// validateConsoleCables is the graph check of the console cables (rules 9-11 and 14): each end is a port of its device, one
// serial and one console end on two devices, a port on one cable at most, and at most MaxConsoleCablesPerLab cables. The
// network connections never resolve a port name (they resolve interfaces only), so a port is never a network end.
func (t Topology) validateConsoleCables(byID map[uuid.UUID]Device) error {
	type portKey struct {
		dev  uuid.UUID
		port string
	}
	used := make(map[portKey]bool)
	cables := 0
	for ci, c := range t.Connections {
		if !c.IsConsole() {
			continue
		}
		cables++
		if cables > MaxConsoleCablesPerLab {
			return ErrConsoleCableLimit.WithPublicContext("connection", ci+1).
				WithPublicContext("limit", MaxConsoleCablesPerLab).
				Err()
		}
		if reason := consoleCableEndsInvalid(c, byID); reason != "" {
			return ErrConsoleCableEndsInvalid.WithPublicContext("connection", ci+1).
				WithPublicContext("reason", reason).
				Err()
		}
		for _, ep := range c.Endpoints {
			key := portKey{dev: ep.DeviceID, port: ep.Interface}
			if used[key] {
				return ErrConsolePortInUse.WithPublicContext("connection", ci+1).
					WithPublicContext("device", byID[ep.DeviceID].Name).
					WithPublicContext("port", ep.Interface).
					Err()
			}
			used[key] = true
		}
	}
	return nil
}

// consoleCableEndsInvalid says why the ends of a console cable do not make a line, "" when they do.
func consoleCableEndsInvalid(c Connection, byID map[uuid.UUID]Device) string {
	var kinds [2]PortKind
	for i, ep := range c.Endpoints {
		d, ok := byID[ep.DeviceID]
		if !ok || ep.Kind != EndpointDevice {
			return "unresolved"
		}
		switch kind := d.PortOf(ep.Interface); kind {
		case PortNIC:
			return "nic"
		case PortNone:
			return "unresolved"
		default:
			kinds[i] = kind
		}
	}
	switch {
	case c.Endpoints[0].DeviceID == c.Endpoints[1].DeviceID:
		return "same_device"
	case kinds[0] == PortSerial && kinds[1] == PortSerial:
		return "two_serial"
	case kinds[0] == PortConsole && kinds[1] == PortConsole:
		return "two_console"
	}
	return ""
}
