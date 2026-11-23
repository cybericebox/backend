package exerciseModel

import "testing"

// operatedKernelLog is the operated device of a defender: a microVM with the kernel log on its first console port, console0.
func operatedKernelLog(t *testing.T, ports ...string) Topology {
	t.Helper()
	topo, vpn, web := toolsBase(t, func(vpn, web Device) []Connection {
		return []Connection{link(port(vpn, GatewayPort), port(web, "eth0"))}
	})
	_ = vpn
	web.Profile, web.KernelLog = NodeProfileMicroVM, true
	for _, name := range ports {
		web.ConsolePorts = append(web.ConsolePorts, ConsolePort{Name: name})
	}
	topo.Devices[1] = web
	return topo
}

func cabledConsolePort(t *testing.T, topo Topology) string {
	t.Helper()
	out, tools, err := topo.WithToolsDevices([]ToolsPlacement{{Image: "tools:1", Operates: []string{"web"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range out.Connections {
		if c.IsConsole() && c.Endpoints[0].DeviceID == tools[0].ID {
			return c.Endpoints[1].Interface
		}
	}
	return ""
}

// The console line of an opponent avoids the port that carries the kernel log while another console port is free.
func TestCableOperatedPrefersAPortWithoutTheKernelLog(t *testing.T) {
	if got := cabledConsolePort(t, operatedKernelLog(t, "console0", "aux0")); got != "aux0" {
		t.Fatalf("cabled to %q, want aux0 (console0 carries the kernel log)", got)
	}
}

// With no other free console port, the kernel-log port is used: the line then carries kernel messages too.
func TestCableOperatedUsesTheKernelLogPortWhenItIsTheOnlyOne(t *testing.T) {
	if got := cabledConsolePort(t, operatedKernelLog(t, "console0")); got != "console0" {
		t.Fatalf("cabled to %q, want console0", got)
	}
}
