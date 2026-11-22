package exerciseModel_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

// consoleContext is the public context of a console error.
func consoleContext(t *testing.T, err error) map[string]any {
	t.Helper()
	var coded interface{ PublicContext() map[string]any }
	require.ErrorAs(t, err, &coded)
	return coded.PublicContext()
}

// router is a node with a NIC and the console ports given; pc a node with a NIC and the serial ports given.
func router(name string, ports ...exerciseModel.ConsolePort) exerciseModel.Device {
	d := dev(name, exerciseModel.DeviceTypeContainer, "eth0")
	d.ConsolePorts = ports
	return d
}

func pc(name string, ports ...exerciseModel.SerialPort) exerciseModel.Device {
	d := dev(name, exerciseModel.DeviceTypeContainer, "eth0")
	d.SerialPorts = ports
	return d
}

func cable(a, b exerciseModel.Endpoint) exerciseModel.Connection {
	return exerciseModel.Connection{Kind: exerciseModel.ConnectionConsole, Endpoints: []exerciseModel.Endpoint{a, b}}
}

func structureOf(topo exerciseModel.Topology) error {
	version := exerciseModel.ExerciseVersion{Variants: []exerciseModel.Variant{publishVariant(topo)}}
	return version.ValidateStructure()
}

func publishOf(topo exerciseModel.Topology) error {
	version := exerciseModel.ExerciseVersion{Variants: []exerciseModel.Variant{publishVariant(topo)}}
	return version.ValidateForPublish()
}

func TestConsolePorts_ValidShapes(t *testing.T) {
	r1 := router("r1",
		exerciseModel.ConsolePort{Name: "console0", Command: []string{"/usr/bin/vtysh"}},
		exerciseModel.ConsolePort{Name: "aux0"},
		exerciseModel.ConsolePort{Name: "console1", Native: &exerciseModel.NativeSource{Kind: exerciseModel.NativeTCP, Port: 2001}},
		exerciseModel.ConsolePort{Name: "console15", Native: &exerciseModel.NativeSource{Kind: exerciseModel.NativeStdio}},
	)
	pc1 := pc("pc1", exerciseModel.SerialPort{Name: "serial0"}, exerciseModel.SerialPort{Name: "serial1", Path: "/dev/ttyUSB0"})
	topo := exerciseModel.Topology{
		Devices: []exerciseModel.Device{r1, pc1},
		Connections: []exerciseModel.Connection{
			cable(devEP(pc1, "serial0"), devEP(r1, "console0")),
			cable(devEP(r1, "aux0"), devEP(pc1, "serial1")), // the order of the ends is free
			conn(devEP(pc1, "eth0"), devEP(r1, "eth0")),
		},
	}
	require.NoError(t, structureOf(topo))
	require.NoError(t, publishOf(topo))
	v := exerciseModel.Variant{Topology: topo}
	require.NoError(t, v.ValidateTopologyForDeploy())
}

func TestConsolePorts_UnpluggedPortIsValid(t *testing.T) {
	topo := exerciseModel.Topology{Devices: []exerciseModel.Device{
		router("r1", exerciseModel.ConsolePort{Name: "console0"}),
		pc("pc1", exerciseModel.SerialPort{Name: "serial0"}),
	}}
	require.NoError(t, publishOf(topo))
}

func TestConsolePorts_DeviceRules(t *testing.T) {
	windows := router("win", exerciseModel.ConsolePort{Name: "console0"})
	windows.Profile, windows.System = exerciseModel.NodeProfileVM, exerciseModel.SystemWindows
	sw := dev("sw", exerciseModel.DeviceTypeUnmanagedSwitch)
	sw.SerialPorts = []exerciseModel.SerialPort{{Name: "serial0"}}
	vpn := gw("vpn", exerciseModel.DeviceTypeVPNGateway)
	vpn.ConsolePorts = []exerciseModel.ConsolePort{{Name: "console0"}}
	hub := dev("hub", exerciseModel.DeviceTypeHub)
	hub.ConsolePorts = []exerciseModel.ConsolePort{{Name: "console0"}}

	many := func(n int, serial bool) exerciseModel.Device {
		d := dev("many", exerciseModel.DeviceTypeContainer, "eth0")
		for i := range n {
			if serial {
				d.SerialPorts = append(d.SerialPorts, exerciseModel.SerialPort{Name: "serial" + itoa(i)})
			} else {
				d.ConsolePorts = append(d.ConsolePorts, exerciseModel.ConsolePort{Name: "console" + itoa(i)})
			}
		}
		return d
	}
	long := strings.Repeat("a", exerciseModel.MaxCommandEntryBytes+1)
	tooMany := make([]string, exerciseModel.MaxCommandEntries+1)
	for i := range tooMany {
		tooMany[i] = "/bin/x"
	}
	nic := dev("nic", exerciseModel.DeviceTypeContainer, "console0")
	nic.ConsolePorts = []exerciseModel.ConsolePort{{Name: "console0"}}

	for _, tc := range []struct {
		name    string
		device  exerciseModel.Device
		want    error
		context map[string]any
	}{
		{"ports on a switch", sw, exerciseModel.ErrConsolePortNotAllowed.Err(), map[string]any{"device": "sw", "field": "serial_ports", "reason": "device_type"}},
		{"ports on a hub", hub, exerciseModel.ErrConsolePortNotAllowed.Err(), map[string]any{"device": "hub", "field": "console_ports", "reason": "device_type"}},
		{"ports on a gateway", vpn, exerciseModel.ErrConsolePortNotAllowed.Err(), map[string]any{"device": "vpn", "reason": "device_type"}},
		{"ports on windows", windows, exerciseModel.ErrConsolePortNotAllowed.Err(), map[string]any{"device": "win", "reason": "system"}},
		{"bad console name", router("r", exerciseModel.ConsolePort{Name: "con0"}), exerciseModel.ErrConsolePortInvalid.Err(), map[string]any{"device": "r", "field": "console_ports[1].name", "reason": "name"}},
		{"console name with a leading zero", router("r", exerciseModel.ConsolePort{Name: "console01"}), exerciseModel.ErrConsolePortInvalid.Err(), map[string]any{"reason": "name"}},
		{"console name over 99", router("r", exerciseModel.ConsolePort{Name: "aux100"}), exerciseModel.ErrConsolePortInvalid.Err(), map[string]any{"reason": "name"}},
		{"bad serial name", pc("p", exerciseModel.SerialPort{Name: "ttyS0"}), exerciseModel.ErrSerialPortInvalid.Err(), map[string]any{"device": "p", "field": "serial_ports[1].name", "reason": "name"}},
		{"duplicate console name", router("r", exerciseModel.ConsolePort{Name: "console0"}, exerciseModel.ConsolePort{Name: "console0"}), exerciseModel.ErrDevicePortNameTaken.Err(), map[string]any{"device": "r", "field": "console_ports[2].name", "port": "console0"}},
		{"duplicate serial name", pc("p", exerciseModel.SerialPort{Name: "serial0"}, exerciseModel.SerialPort{Name: "serial0"}), exerciseModel.ErrDevicePortNameTaken.Err(), map[string]any{"field": "serial_ports[2].name"}},
		{"a port named like a nic", nic, exerciseModel.ErrDevicePortNameTaken.Err(), map[string]any{"device": "nic", "field": "console_ports[1].name", "port": "console0"}},
		{"too many console ports", many(exerciseModel.MaxConsolePorts+1, false), exerciseModel.ErrDevicePortLimit.Err(), map[string]any{"device": "many", "kind": "console", "limit": exerciseModel.MaxConsolePorts, "field": "console_ports"}},
		{"too many serial ports", many(exerciseModel.MaxSerialPorts+1, true), exerciseModel.ErrDevicePortLimit.Err(), map[string]any{"kind": "serial", "limit": exerciseModel.MaxSerialPorts, "field": "serial_ports"}},
		{"relative command", router("r", exerciseModel.ConsolePort{Name: "console0", Command: []string{"vtysh"}}), exerciseModel.ErrConsolePortInvalid.Err(), map[string]any{"field": "console_ports[1].command", "reason": "command"}},
		{"command under /proc", router("r", exerciseModel.ConsolePort{Name: "console0", Command: []string{"/proc/self/exe"}}), exerciseModel.ErrConsolePortInvalid.Err(), map[string]any{"reason": "command"}},
		{"command under /sys", router("r", exerciseModel.ConsolePort{Name: "console0", Command: []string{"/usr/../sys/x"}}), exerciseModel.ErrConsolePortInvalid.Err(), map[string]any{"reason": "command"}},
		{"command entry too long", router("r", exerciseModel.ConsolePort{Name: "console0", Command: []string{"/bin/sh", long}}), exerciseModel.ErrConsolePortInvalid.Err(), map[string]any{"reason": "command"}},
		{"command with a nul", router("r", exerciseModel.ConsolePort{Name: "console0", Command: []string{"/bin/sh", "a\x00b"}}), exerciseModel.ErrConsolePortInvalid.Err(), map[string]any{"reason": "command"}},
		{"too many command entries", router("r", exerciseModel.ConsolePort{Name: "console0", Command: tooMany}), exerciseModel.ErrConsolePortInvalid.Err(), map[string]any{"reason": "command"}},
		{"bad user", router("r", exerciseModel.ConsolePort{Name: "console0", User: "Root User"}), exerciseModel.ErrConsolePortInvalid.Err(), map[string]any{"field": "console_ports[1].user", "reason": "user"}},
		{"command and native", router("r", exerciseModel.ConsolePort{Name: "console0", Command: []string{"/bin/login"}, Native: &exerciseModel.NativeSource{Kind: exerciseModel.NativeStdio}}), exerciseModel.ErrConsolePortInvalid.Err(), map[string]any{"field": "console_ports[1].native", "reason": "native"}},
		{"user on a native line", router("r", exerciseModel.ConsolePort{Name: "console0", User: "root", Native: &exerciseModel.NativeSource{Kind: exerciseModel.NativeStdio}}), exerciseModel.ErrConsolePortInvalid.Err(), map[string]any{"reason": "user"}},
		{"unknown native kind", router("r", exerciseModel.ConsolePort{Name: "console0", Native: &exerciseModel.NativeSource{Kind: "telnet"}}), exerciseModel.ErrConsolePortInvalid.Err(), map[string]any{"reason": "native"}},
		{"vm-serial is the platform's", router("r", exerciseModel.ConsolePort{Name: "console0", Native: &exerciseModel.NativeSource{Kind: exerciseModel.NativeVMSerial}}), exerciseModel.ErrConsolePortInvalid.Err(), map[string]any{"reason": "native"}},
		{"tcp without a port", router("r", exerciseModel.ConsolePort{Name: "console0", Native: &exerciseModel.NativeSource{Kind: exerciseModel.NativeTCP}}), exerciseModel.ErrConsolePortInvalid.Err(), map[string]any{"reason": "native"}},
		{"tcp port too high", router("r", exerciseModel.ConsolePort{Name: "console0", Native: &exerciseModel.NativeSource{Kind: exerciseModel.NativeTCP, Port: 65536}}), exerciseModel.ErrConsolePortInvalid.Err(), map[string]any{"reason": "native"}},
		{"stdio with a port", router("r", exerciseModel.ConsolePort{Name: "console0", Native: &exerciseModel.NativeSource{Kind: exerciseModel.NativeStdio, Port: 1}}), exerciseModel.ErrConsolePortInvalid.Err(), map[string]any{"reason": "native"}},
		{"stdio twice", router("r",
			exerciseModel.ConsolePort{Name: "console0", Native: &exerciseModel.NativeSource{Kind: exerciseModel.NativeStdio}},
			exerciseModel.ConsolePort{Name: "aux0", Native: &exerciseModel.NativeSource{Kind: exerciseModel.NativeStdio}}),
			exerciseModel.ErrConsolePortInvalid.Err(), map[string]any{"field": "console_ports[2].native", "reason": "stdio_twice"}},
		{"bad serial path", pc("p", exerciseModel.SerialPort{Name: "serial0", Path: "/tmp/tty"}), exerciseModel.ErrSerialPortInvalid.Err(), map[string]any{"field": "serial_ports[1].path", "reason": "path"}},
		{"serial path /dev/console", pc("p", exerciseModel.SerialPort{Name: "serial0", Path: "/dev/console"}), exerciseModel.ErrSerialPortInvalid.Err(), map[string]any{"reason": "path"}},
		{"serial path /dev/tty", pc("p", exerciseModel.SerialPort{Name: "serial0", Path: "/dev/tty"}), exerciseModel.ErrSerialPortInvalid.Err(), map[string]any{"reason": "path"}},
		{"serial path /dev/ptmx", pc("p", exerciseModel.SerialPort{Name: "serial0", Path: "/dev/ptmx"}), exerciseModel.ErrSerialPortInvalid.Err(), map[string]any{"reason": "path"}},
		{"serial path too long", pc("p", exerciseModel.SerialPort{Name: "serial0", Path: "/dev/ttyA" + strings.Repeat("b", 15)}), exerciseModel.ErrSerialPortInvalid.Err(), map[string]any{"reason": "path"}},
		{"two serial ports on one path", pc("p", exerciseModel.SerialPort{Name: "serial0", Path: "/dev/ttyCON1"}, exerciseModel.SerialPort{Name: "serial1"}), exerciseModel.ErrSerialPortInvalid.Err(), map[string]any{"field": "serial_ports[2].path", "reason": "path"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := structureOf(exerciseModel.Topology{Devices: []exerciseModel.Device{tc.device}})
			require.ErrorIs(t, err, tc.want)
			got := consoleContext(t, err)
			assert.Equal(t, 1, got["variant"], "the variant is named")
			for key, value := range tc.context {
				assert.Equal(t, value, got[key], key)
			}
		})
	}
}

func TestConsoleConnections_KindIsConsoleOrEmpty(t *testing.T) {
	a, b := dev("a", exerciseModel.DeviceTypeContainer, "eth0"), dev("b", exerciseModel.DeviceTypeContainer, "eth0")
	bad := conn(devEP(a, "eth0"), devEP(b, "eth0"))
	bad.Kind = "serial"
	err := structureOf(exerciseModel.Topology{Devices: []exerciseModel.Device{a, b}, Connections: []exerciseModel.Connection{conn(devEP(a, "eth0"), devEP(b, "eth0")), bad}})
	require.ErrorIs(t, err, exerciseModel.ErrConnectionKindInvalid.Err())
	assert.Equal(t, 2, consoleContext(t, err)["connection"])
	assert.Equal(t, "kind", consoleContext(t, err)["field"])

	one := cable(devEP(a, "eth0"), devEP(b, "eth0"))
	one.Endpoints = one.Endpoints[:1]
	require.ErrorIs(t, structureOf(exerciseModel.Topology{Devices: []exerciseModel.Device{a, b}, Connections: []exerciseModel.Connection{one}}), exerciseModel.ErrConnectionArityInvalid.Err())
}

func TestConsoleCables_GraphRules(t *testing.T) {
	r1 := router("r1", exerciseModel.ConsolePort{Name: "console0"}, exerciseModel.ConsolePort{Name: "aux0"})
	r2 := router("r2", exerciseModel.ConsolePort{Name: "console0"})
	pc1 := pc("pc1", exerciseModel.SerialPort{Name: "serial0"}, exerciseModel.SerialPort{Name: "serial1"})
	both := router("both", exerciseModel.ConsolePort{Name: "console0"})
	both.SerialPorts = []exerciseModel.SerialPort{{Name: "serial0"}}
	sw := dev("sw", exerciseModel.DeviceTypeUnmanagedSwitch)
	ghost := router("ghost", exerciseModel.ConsolePort{Name: "console0"})
	devices := []exerciseModel.Device{r1, r2, pc1, both, sw}

	for _, tc := range []struct {
		name    string
		conns   []exerciseModel.Connection
		want    error
		context map[string]any
	}{
		{"unknown port", []exerciseModel.Connection{cable(devEP(pc1, "serial0"), devEP(r1, "console9"))}, exerciseModel.ErrConsoleCableEndsInvalid.Err(), map[string]any{"connection": 1, "reason": "unresolved"}},
		{"unknown device", []exerciseModel.Connection{cable(devEP(pc1, "serial0"), devEP(ghost, "console0"))}, exerciseModel.ErrConsoleCableEndsInvalid.Err(), map[string]any{"reason": "unresolved"}},
		{"a nic end", []exerciseModel.Connection{cable(devEP(pc1, "eth0"), devEP(r1, "console0"))}, exerciseModel.ErrConsoleCableEndsInvalid.Err(), map[string]any{"reason": "nic"}},
		{"a switch port end", []exerciseModel.Connection{cable(devEP(sw, "GigabitEthernet0/1"), devEP(r1, "console0"))}, exerciseModel.ErrConsoleCableEndsInvalid.Err(), map[string]any{"reason": "nic"}},
		{"two serial ends", []exerciseModel.Connection{cable(devEP(pc1, "serial0"), devEP(both, "serial0"))}, exerciseModel.ErrConsoleCableEndsInvalid.Err(), map[string]any{"reason": "two_serial"}},
		{"two console ends", []exerciseModel.Connection{cable(devEP(r1, "console0"), devEP(r2, "console0"))}, exerciseModel.ErrConsoleCableEndsInvalid.Err(), map[string]any{"reason": "two_console"}},
		{"same device", []exerciseModel.Connection{cable(devEP(both, "serial0"), devEP(both, "console0"))}, exerciseModel.ErrConsoleCableEndsInvalid.Err(), map[string]any{"reason": "same_device"}},
		{"console port used twice", []exerciseModel.Connection{
			cable(devEP(pc1, "serial0"), devEP(r1, "console0")),
			cable(devEP(pc1, "serial1"), devEP(r1, "console0")),
		}, exerciseModel.ErrConsolePortInUse.Err(), map[string]any{"connection": 2, "device": "r1", "port": "console0"}},
		{"serial port used twice", []exerciseModel.Connection{
			cable(devEP(pc1, "serial0"), devEP(r1, "console0")),
			cable(devEP(pc1, "serial0"), devEP(r2, "console0")),
		}, exerciseModel.ErrConsolePortInUse.Err(), map[string]any{"device": "pc1", "port": "serial0"}},
		{"a port on a network connection", []exerciseModel.Connection{conn(devEP(pc1, "serial0"), devEP(r1, "console0"))}, exerciseModel.ErrConsolePortOnNetwork.Err(), map[string]any{"connection": 1, "device": "pc1", "port": "serial0", "field": "kind"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := publishOf(exerciseModel.Topology{Devices: devices, Connections: tc.conns})
			require.ErrorIs(t, err, tc.want)
			if tc.context == nil {
				return
			}
			got := consoleContext(t, err)
			assert.Equal(t, 1, got["variant"])
			for key, value := range tc.context {
				assert.Equal(t, value, got[key], key)
			}
		})
	}
}

// The graph rules run on deploy too.
func TestConsoleCables_DeployRunsTheGraphRules(t *testing.T) {
	r1 := router("r1", exerciseModel.ConsolePort{Name: "console0"})
	r2 := router("r2", exerciseModel.ConsolePort{Name: "console0"})
	v := exerciseModel.Variant{Topology: exerciseModel.Topology{
		Devices:     []exerciseModel.Device{r1, r2},
		Connections: []exerciseModel.Connection{cable(devEP(r1, "console0"), devEP(r2, "console0"))},
	}}
	require.ErrorIs(t, v.ValidateTopologyForDeploy(), exerciseModel.ErrConsoleCableEndsInvalid.Err())
}

func TestConsoleCables_Limit(t *testing.T) {
	var devices []exerciseModel.Device
	var conns []exerciseModel.Connection
	for i := 0; i <= exerciseModel.MaxConsoleCablesPerLab; i++ {
		r := router("r"+itoa(i), exerciseModel.ConsolePort{Name: "console0"})
		p := pc("p"+itoa(i), exerciseModel.SerialPort{Name: "serial0"})
		devices = append(devices, r, p)
		conns = append(conns, cable(devEP(p, "serial0"), devEP(r, "console0")))
	}
	topo := exerciseModel.Topology{Devices: devices, Connections: conns[:exerciseModel.MaxConsoleCablesPerLab]}
	require.NoError(t, publishOf(topo))
	topo.Connections = conns
	err := publishOf(topo)
	require.ErrorIs(t, err, exerciseModel.ErrConsoleCableLimit.Err())
	assert.Equal(t, exerciseModel.MaxConsoleCablesPerLab, consoleContext(t, err)["limit"])
}

// A console cable never joins two networks: the gateway rules do not follow it.
func TestConsoleCables_LeftOutOfTheL2Graph(t *testing.T) {
	r1 := router("r1", exerciseModel.ConsolePort{Name: "console0"})
	pc1 := pc("pc1", exerciseModel.SerialPort{Name: "serial0"})
	vpn := gw("vpn", exerciseModel.DeviceTypeVPNGateway)
	out := gw("out", exerciseModel.DeviceTypeInternetGateway)
	topo := exerciseModel.Topology{
		Devices: []exerciseModel.Device{r1, pc1, vpn, out},
		Connections: []exerciseModel.Connection{
			conn(devEP(vpn, "eth0"), devEP(r1, "eth0")),
			conn(devEP(out, "eth0"), devEP(pc1, "eth0")),
			cable(devEP(pc1, "serial0"), devEP(r1, "console0")),
		},
	}
	require.NoError(t, publishOf(topo), "two gateways joined only by a console cable are two networks")

	host := pc("host", exerciseModel.SerialPort{Name: "serial0"})
	host.Interfaces[0].IP = exerciseModel.IPConfig{Type: exerciseModel.IPConfigTypeStatic, AddressRef: &exerciseModel.NetworkIPRef{Gateway: "vpn", Host: 10}}
	reach := exerciseModel.Topology{
		Devices: []exerciseModel.Device{host, r1, vpn},
		Connections: []exerciseModel.Connection{
			conn(devEP(vpn, "eth0"), devEP(r1, "eth0")),
			cable(devEP(host, "serial0"), devEP(r1, "console0")),
		},
	}
	require.ErrorIs(t, publishOf(reach), exerciseModel.ErrDeviceAddressRefUnreachable.Err(), "an address is not reached over a console cable")
}

func TestDevice_PortOf(t *testing.T) {
	r := router("r", exerciseModel.ConsolePort{Name: "console0"})
	r.SerialPorts = []exerciseModel.SerialPort{{Name: "serial3"}}
	assert.Equal(t, exerciseModel.PortConsole, r.PortOf("console0"))
	assert.Equal(t, exerciseModel.PortSerial, r.PortOf("serial3"))
	assert.Equal(t, exerciseModel.PortNIC, r.PortOf("eth0"))
	assert.Equal(t, exerciseModel.PortNone, r.PortOf("aux0"))
	assert.Equal(t, exerciseModel.PortNIC, gw("g", exerciseModel.DeviceTypeVPNGateway).PortOf("eth0"))
	assert.Equal(t, exerciseModel.PortNIC, dev("s", exerciseModel.DeviceTypeHub).PortOf("GigabitEthernet0/2"))
}

func TestSerialPort_DevicePath(t *testing.T) {
	assert.Equal(t, "/dev/ttyCON0", exerciseModel.SerialPort{Name: "serial0"}.DevicePath())
	assert.Equal(t, "/dev/ttyCON12", exerciseModel.SerialPort{Name: "serial12"}.DevicePath())
	assert.Equal(t, "/dev/ttyUSB0", exerciseModel.SerialPort{Name: "serial0", Path: "/dev/ttyUSB0"}.DevicePath())
}

func TestTopology_UsesConsoleLines(t *testing.T) {
	assert.False(t, exerciseModel.Topology{Devices: []exerciseModel.Device{dev("a", exerciseModel.DeviceTypeContainer, "eth0")}}.UsesConsoleLines())
	assert.True(t, exerciseModel.Topology{Devices: []exerciseModel.Device{router("r", exerciseModel.ConsolePort{Name: "console0"})}}.UsesConsoleLines())
	assert.True(t, exerciseModel.Topology{Devices: []exerciseModel.Device{pc("p", exerciseModel.SerialPort{Name: "serial0"})}}.UsesConsoleLines())
	assert.True(t, exerciseModel.Topology{Connections: []exerciseModel.Connection{{Kind: exerciseModel.ConnectionConsole}}}.UsesConsoleLines())
}

// Stored topologies without the keys read and write as before; the new keys are omitted when empty.
func TestConsoleKeys_OmittedWhenEmpty(t *testing.T) {
	d := dev("a", exerciseModel.DeviceTypeContainer, "eth0")
	c := conn(devEP(d, "eth0"), exerciseModel.Endpoint{Kind: exerciseModel.EndpointDevice, DeviceID: uuid.Must(uuid.NewV7()), Interface: "eth0"})
	for _, value := range []any{d, c} {
		text := mustJSON(t, value)
		for _, key := range []string{"console_ports", "serial_ports", "\"kind\":\"\""} {
			assert.NotContains(t, text, key)
		}
	}
	assert.Contains(t, mustJSON(t, cable(c.Endpoints[0], c.Endpoints[1])), "\"kind\":\"console\"")
}

func itoa(n int) string { return strconv.Itoa(n) }

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return string(data)
}


// The guard of every save: a network connection on a name of a port that is no interface of the node (the editor dropped
// the ports and the kind), an unknown kind; a NIC named like a port is a NIC.
func TestValidateConsoleLines(t *testing.T) {
	pcNoPorts := dev("pc1", exerciseModel.DeviceTypeContainer, "eth0")
	r1 := dev("r1", exerciseModel.DeviceTypeContainer, "eth0")
	lost := exerciseModel.Variant{Topology: exerciseModel.Topology{
		Devices:     []exerciseModel.Device{pcNoPorts, r1},
		Connections: []exerciseModel.Connection{conn(devEP(pcNoPorts, "eth0"), devEP(r1, "eth0")), conn(devEP(pcNoPorts, "serial0"), devEP(r1, "console0"))},
	}}
	err := exerciseModel.ValidateConsoleLines([]exerciseModel.Variant{{}, lost})
	require.ErrorIs(t, err, exerciseModel.ErrConsolePortOnNetwork.Err())
	got := consoleContext(t, err)
	assert.Equal(t, 2, got["variant"])
	assert.Equal(t, 2, got["connection"])
	assert.Equal(t, "kind", got["field"])

	unknown := exerciseModel.Variant{Topology: exerciseModel.Topology{Connections: []exerciseModel.Connection{{Kind: "foo"}}}}
	require.ErrorIs(t, exerciseModel.ValidateConsoleLines([]exerciseModel.Variant{unknown}), exerciseModel.ErrConnectionKindInvalid.Err())

	odd := dev("odd", exerciseModel.DeviceTypeContainer, "serial0")
	nic := exerciseModel.Variant{Topology: exerciseModel.Topology{
		Devices:     []exerciseModel.Device{odd, r1},
		Connections: []exerciseModel.Connection{conn(devEP(odd, "serial0"), devEP(r1, "eth0"))},
	}}
	require.NoError(t, exerciseModel.ValidateConsoleLines([]exerciseModel.Variant{nic}))

	pc1 := pc("pc1", exerciseModel.SerialPort{Name: "serial0"})
	rc := router("rc", exerciseModel.ConsolePort{Name: "console0"})
	fine := exerciseModel.Variant{Topology: exerciseModel.Topology{
		Devices:     []exerciseModel.Device{pc1, rc},
		Connections: []exerciseModel.Connection{cable(devEP(pc1, "serial0"), devEP(rc, "console0"))},
	}}
	require.NoError(t, exerciseModel.ValidateConsoleLines([]exerciseModel.Variant{fine}))
}
