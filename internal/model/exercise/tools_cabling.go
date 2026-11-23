package exerciseModel

import (
	"slices"
	"strconv"
)

// Auto-cabling of the tools device of a bot that operates devices (console design, section 8). The symmetry rule: a human
// defender sits at a PC of the author's topology whose serial ports are cabled to console ports; with a seat the bot takes that
// very place, without one the platform gives it the equivalent. Both reach a console through a serial character device and a
// cable; neither has a path the other lacks. A human-defender run never gets here: it deploys the seat as authored.

// takeSeat puts the tools device in the place of the seat (8.2): the connection of the seat's first data NIC is re-pointed to
// tools/eth0 and eth0 takes that NIC's IP configuration; the tools device gets the seat's serial ports with the same names and
// paths, and every console cable of the seat is re-pointed port for port; the seat's other connections and the seat itself are
// removed. The tools device is not in the topology yet (the caller appends it).
func (t *Topology) takeSeat(tools *Device, name string) error {
	at := slices.IndexFunc(t.Devices, func(d Device) bool { return d.Name == name && d.Type.IsNode() })
	if at < 0 {
		return ErrToolsNoSeat
	}
	seat := t.Devices[at]
	nic, ok := t.firstDataNIC(seat)
	if !ok {
		return ErrToolsNoSeat
	}
	for _, iface := range seat.Interfaces {
		if iface.Name == nic {
			tools.Interfaces[0].IP = iface.IP.clone()
		}
	}
	tools.SerialPorts = slices.Clone(seat.SerialPorts)
	kept := make([]Connection, 0, len(t.Connections))
	moved := false
	for _, c := range t.Connections {
		end := slices.IndexFunc(c.Endpoints, func(ep Endpoint) bool { return ep.Kind == EndpointDevice && ep.DeviceID == seat.ID })
		switch {
		case end < 0:
		case c.IsConsole():
			c.Endpoints[end] = Endpoint{Kind: EndpointDevice, DeviceID: tools.ID, Interface: c.Endpoints[end].Interface}
		case !moved && c.Endpoints[end].Interface == nic:
			c.Endpoints[end] = Endpoint{Kind: EndpointDevice, DeviceID: tools.ID, Interface: tools.Interfaces[0].Name}
			moved = true
		default:
			continue // another NIC of the seat: it drops with the device
		}
		kept = append(kept, c)
	}
	t.Connections = kept
	t.Devices = slices.Delete(t.Devices, at, at+1)
	return nil
}

// cableOperated gives the tools device a console line to each operated device that declares a console port (8.3): in the order
// of operates, the next serial port of the tools device (serial0, serial1, ...; the default path /dev/ttyCON<n>) and a console
// cable to the first free console port of that device, in the order the device declares them. A port already cabled (by the
// author, or for another tools device) is never re-plugged; a device with no free console port, or none at all, is reached over
// the lab network only. More than MaxSerialPorts lines is ErrToolsNoSerialPort.
func (t *Topology) cableOperated(tools *Device, operates []string) error {
	type portKey struct {
		device string
		port   string
	}
	used := map[portKey]bool{}
	for _, c := range t.Connections {
		if !c.IsConsole() {
			continue
		}
		for _, ep := range c.Endpoints {
			used[portKey{ep.DeviceID.String(), ep.Interface}] = true
		}
	}
	for _, name := range operates {
		at := slices.IndexFunc(t.Devices, func(d Device) bool { return d.Name == name && d.Type.IsNode() })
		if at < 0 {
			continue
		}
		device := t.Devices[at]
		free := slices.IndexFunc(device.ConsolePorts, func(p ConsolePort) bool { return !used[portKey{device.ID.String(), p.Name}] })
		if free < 0 {
			continue
		}
		if len(tools.SerialPorts) >= MaxSerialPorts {
			return ErrToolsNoSerialPort
		}
		serial := SerialPort{Name: "serial" + strconv.Itoa(len(tools.SerialPorts))}
		tools.SerialPorts = append(tools.SerialPorts, serial)
		console := device.ConsolePorts[free].Name
		used[portKey{device.ID.String(), console}] = true
		t.Connections = append(t.Connections, Connection{Kind: ConnectionConsole, Endpoints: []Endpoint{
			{Kind: EndpointDevice, DeviceID: tools.ID, Interface: serial.Name},
			{Kind: EndpointDevice, DeviceID: device.ID, Interface: console},
		}})
	}
	return nil
}

// clone is a copy of the configuration that shares no slice with it.
func (c IPConfig) clone() IPConfig {
	out := c
	out.Addresses = slices.Clone(c.Addresses)
	out.Routes = slices.Clone(c.Routes)
	return out
}
