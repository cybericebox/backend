package exerciseModel

import (
	"github.com/cybericebox/backend/internal/model"
	"github.com/cybericebox/backend/pkg/err"
)

// Errors of console ports and console cables. Each carries the public context variant (1-based position in the version, added
// by the version checks and the deploy) and device (its name) or connection (1-based position in the variant's connections),
// and field where the editor marks a field (console_ports[2].command, serial_ports[1].path, kind).
// Detail 11 is reserved for ErrDefenderInvalid (the defender block of the dossier, a later phase).
var (
	// ErrConsolePortInvalid: a console port breaks its rules; reason is name, command, user, native or stdio_twice.
	ErrConsolePortInvalid = err.ErrInvalidData.WithObjectCode(model.ConsoleObjectCode).
				WithMessage("Invalid console port").WithDetailCode(1)
	// ErrSerialPortInvalid: a serial port breaks its rules; reason is name or path.
	ErrSerialPortInvalid = err.ErrInvalidData.WithObjectCode(model.ConsoleObjectCode).
				WithMessage("Invalid serial port").WithDetailCode(2)
	// ErrDevicePortNameTaken: two ports of a device share a name, or a port is named like an interface of the device; port
	// names the port.
	ErrDevicePortNameTaken = err.ErrInvalidData.WithObjectCode(model.ConsoleObjectCode).
				WithMessage("The device already has a port or an interface with this name").WithDetailCode(3)
	// ErrDevicePortLimit: a device has too many ports of one kind; kind is serial or console, limit the most it may have.
	ErrDevicePortLimit = err.ErrInvalidData.WithObjectCode(model.ConsoleObjectCode).
				WithMessage("The device has too many ports of this kind").WithDetailCode(4)
	// ErrConsolePortNotAllowed: ports on a switch, a hub, a gateway or a Windows device; reason is device_type or system.
	ErrConsolePortNotAllowed = err.ErrInvalidData.WithObjectCode(model.ConsoleObjectCode).
					WithMessage("This device cannot have console or serial ports").WithDetailCode(5)
	// ErrConnectionKindInvalid: a connection kind other than empty (a network connection) or console.
	ErrConnectionKindInvalid = err.ErrInvalidData.WithObjectCode(model.ConsoleObjectCode).
					WithMessage("Unknown connection kind").WithDetailCode(6)
	// ErrConsoleCableEndsInvalid: the ends of a console cable are not one serial and one console port of two devices; reason
	// is unresolved, two_serial, two_console, same_device or nic.
	ErrConsoleCableEndsInvalid = err.ErrInvalidData.WithObjectCode(model.ConsoleObjectCode).
					WithMessage("A console cable joins one serial port and one console port of two devices").
					WithDetailCode(7)
	// ErrConsolePortInUse: a second cable on a port; device and port name it.
	ErrConsolePortInUse = err.ErrInvalidData.WithObjectCode(model.ConsoleObjectCode).
				WithMessage("The port already has a console cable").WithDetailCode(8)
	// ErrConsoleCableLimit: more console cables than a lab may have; limit is the most.
	ErrConsoleCableLimit = err.ErrInvalidData.WithObjectCode(model.ConsoleObjectCode).
				WithMessage("The lab has too many console cables").WithDetailCode(9)
	// ErrConsoleUnavailable: the lab uses console ports or cables and no laboratory agent offers them.
	ErrConsoleUnavailable = err.ErrConflict.WithObjectCode(model.ConsoleObjectCode).
				WithMessage("No connected laboratory agent offers console cables").WithDetailCode(10)
	// ErrConsolePortOnNetwork: a network connection ends on a console or serial port (or a name of one that is no interface
	// of the node); it must be a console cable (kind console). Context connection, device, port and field kind.
	ErrConsolePortOnNetwork = err.ErrInvalidData.WithObjectCode(model.ConsoleObjectCode).
				WithMessage("A connection to a console or serial port must be a console cable").WithDetailCode(12)
)
