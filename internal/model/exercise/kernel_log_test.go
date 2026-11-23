package exerciseModel_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

// microVMRouter is a microVM node with the console ports given and the kernel log asked for.
func microVMRouter(name string, ports ...exerciseModel.ConsolePort) exerciseModel.Device {
	d := router(name, ports...)
	d.Profile = exerciseModel.NodeProfileMicroVM
	d.KernelLog = true
	return d
}

func TestKernelLog_ValidOnAMicroVMWithAConsolePortThatIsNotNative(t *testing.T) {
	d := microVMRouter("r1",
		exerciseModel.ConsolePort{Name: "aux0", Native: &exerciseModel.NativeSource{Kind: exerciseModel.NativeTCP, Port: 2001}},
		exerciseModel.ConsolePort{Name: "console0"},
		exerciseModel.ConsolePort{Name: "console1", Command: []string{"/usr/bin/vtysh"}},
	)
	require.NoError(t, structureOf(exerciseModel.Topology{Devices: []exerciseModel.Device{d}}))
	assert.Equal(t, "console0", d.KernelLogPort(), "the first console port that is not native, in declared order")

	d.KernelLog = false
	assert.Empty(t, d.KernelLogPort(), "without the flag no port carries the kernel log")
}

func TestKernelLog_Rules(t *testing.T) {
	container := microVMRouter("c", exerciseModel.ConsolePort{Name: "console0"})
	container.Profile = exerciseModel.NodeProfileContainer
	unset := microVMRouter("u", exerciseModel.ConsolePort{Name: "console0"})
	unset.Profile = ""
	vm := microVMRouter("v", exerciseModel.ConsolePort{Name: "console0"})
	vm.Profile = exerciseModel.NodeProfileVM
	onlyNative := microVMRouter("n",
		exerciseModel.ConsolePort{Name: "console0", Native: &exerciseModel.NativeSource{Kind: exerciseModel.NativeStdio}},
		exerciseModel.ConsolePort{Name: "aux0", Native: &exerciseModel.NativeSource{Kind: exerciseModel.NativeTCP, Port: 2001}},
	)
	serialOnly := microVMRouter("s")
	serialOnly.SerialPorts = []exerciseModel.SerialPort{{Name: "serial0"}}

	for _, tc := range []struct {
		name   string
		device exerciseModel.Device
		reason string
	}{
		{"on a container", container, "kernel_log_profile"},
		{"on a node without a profile (a container)", unset, "kernel_log_profile"},
		{"on a full vm", vm, "kernel_log_profile"},
		{"without any port", microVMRouter("p"), "kernel_log_no_port"},
		{"with native console ports only", onlyNative, "kernel_log_no_port"},
		{"with serial ports only", serialOnly, "kernel_log_no_port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := structureOf(exerciseModel.Topology{Devices: []exerciseModel.Device{tc.device}})
			require.ErrorIs(t, err, exerciseModel.ErrConsolePortInvalid.Err())
			got := consoleContext(t, err)
			assert.Equal(t, 1, got["variant"])
			assert.Equal(t, tc.device.Name, got["device"])
			assert.Equal(t, "kernel_log", got["field"])
			assert.Equal(t, tc.reason, got["reason"])
		})
	}
}

func TestKernelLog_NeedsAndJSON(t *testing.T) {
	d := microVMRouter("r1", exerciseModel.ConsolePort{Name: "console0"})
	needs := exerciseModel.NeedsOf(exerciseModel.Topology{Devices: []exerciseModel.Device{d}}, nil)
	assert.True(t, needs.KernelLog)
	assert.True(t, needs.ConsoleLines)
	plain := router("r2", exerciseModel.ConsolePort{Name: "console0"})
	assert.False(t, exerciseModel.NeedsOf(exerciseModel.Topology{Devices: []exerciseModel.Device{plain}}, nil).KernelLog)

	raw, err := json.Marshal(d)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"kernel_log":true`)
	raw, err = json.Marshal(plain)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "kernel_log", "the default (false) is not written")
	var back exerciseModel.Device
	require.NoError(t, json.Unmarshal([]byte(`{"name":"r1","type":"node","profile":"microvm","kernel_log":true}`), &back))
	assert.True(t, back.KernelLog)
}
