package exerciseModel

import (
	"encoding/json"
	"slices"

	"github.com/gofrs/uuid"
)

// The dossier of a bot defender reaches its tools device as files (console design 9a.4): root-only, written by the device's
// bootstrap after auto-cabling, so they describe the run's real cabling. The runner of the tools device reads them; the brain
// contract carries none of it and the brain gets no secret.
const (
	DossierConsolesPath    = "/run/eda/consoles.json"
	DossierCredentialsPath = "/run/eda/credentials.json"
	DossierTopologyPath    = "/run/eda/topology.json"
	dossierFileMode        = "0600"
	dossierFileOwner       = "root"
)

type (
	dossierConsolesFile struct {
		Links     []dossierLinkFile `json:"links"`
		Forbidden []string          `json:"forbidden"`
	}
	dossierLinkFile struct {
		SerialPort  string `json:"serial_port"`
		Path        string `json:"path"`
		Device      string `json:"device"`
		ConsoleKind string `json:"console_kind"`
		Program     string `json:"program"`
	}
	dossierCredentialsFile struct {
		Credentials []dossierCredentialFile `json:"credentials"`
	}
	dossierCredentialFile struct {
		Device string `json:"device"`
		Port   string `json:"port,omitempty"`
		User   string `json:"user,omitempty"`
		Value  string `json:"value"`
	}
	// dossierTopologyFile is the run's topology redacted like the participant's view: names instead of ids, no image, no
	// command, no bootstrap, no secret, and none of the platform's control (the control gateways, their switches and the
	// control NIC of a tools device).
	dossierTopologyFile struct {
		Devices     []dossierDeviceFile     `json:"devices"`
		Connections []dossierConnectionFile `json:"connections"`
	}
	dossierDeviceFile struct {
		Name         string                 `json:"name"`
		Type         DeviceType             `json:"type"`
		Interfaces   []dossierInterfaceFile `json:"interfaces,omitempty"`
		ConsolePorts []string               `json:"console_ports,omitempty"`
		SerialPorts  []SerialPort           `json:"serial_ports,omitempty"`
	}
	dossierInterfaceFile struct {
		Name string   `json:"name"`
		IP   IPConfig `json:"ip"`
	}
	dossierConnectionFile struct {
		Kind      ConnectionKind        `json:"kind,omitempty"`
		Endpoints []dossierEndpointFile `json:"endpoints"`
	}
	dossierEndpointFile struct {
		Device    string `json:"device"`
		Interface string `json:"interface,omitempty"`
	}
)

// Files are the dossier of the reader (the tools device) as its bootstrap files: consoles.json and credentials.json always,
// topology.json when the mode is full (the run's topology, redacted) or operates (the reader and what it operates, with their
// links). platform names the platform's own devices of the run (the tools devices): none but the reader is ever listed, so a
// defender never learns where a bot attacker sits. Each file is 0600, owned by root.
func (d Dossier) Files(run Topology, reader Device, platform map[uuid.UUID]bool) ([]BootstrapFile, error) {
	consoles := dossierConsolesFile{Links: []dossierLinkFile{}, Forbidden: nonNil(d.Forbidden)}
	for _, l := range d.Links {
		consoles.Links = append(consoles.Links, dossierLinkFile{
			SerialPort: l.SerialPort, Path: l.Path, Device: l.Device, ConsoleKind: string(l.Kind), Program: l.Program,
		})
	}
	credentials := dossierCredentialsFile{Credentials: []dossierCredentialFile{}}
	for _, c := range d.Credentials {
		credentials.Credentials = append(credentials.Credentials, dossierCredentialFile{Device: c.Device, Port: c.Port, User: c.User, Value: c.Value})
	}
	contents := []struct {
		path string
		v    any
	}{{DossierConsolesPath, consoles}, {DossierCredentialsPath, credentials}}
	if d.Topology != DefenderTopologyNone {
		never := func(dev Device) bool { return platform[dev.ID] && dev.ID != reader.ID }
		listed := func(Device) bool { return true }
		if d.Topology == DefenderTopologyOperates {
			listed = func(dev Device) bool { return dev.ID == reader.ID || slices.Contains(d.Operates, dev.Name) }
		}
		contents = append(contents, struct {
			path string
			v    any
		}{DossierTopologyPath, redactedRunTopology(run, listed, never, d.Topology == DefenderTopologyOperates)})
	}
	out := make([]BootstrapFile, 0, len(contents))
	for _, c := range contents {
		data, err := json.Marshal(c.v)
		if err != nil {
			return nil, err
		}
		out = append(out, BootstrapFile{Path: c.path, Content: string(data), Owner: dossierFileOwner, Group: dossierFileOwner, Mode: dossierFileMode})
	}
	return out, nil
}

// redactedRunTopology is the shape of the run's topology without what a device runs. A device is listed when listed says so;
// a device that never says so is not even named (the platform's control: the control gateways and their switches, with the
// control NIC of a tools device; and the other tools devices of the run). With touching (a subset), a connection is listed
// when one of its ends is listed and none is never-named (the far end is named, not listed); without it, when all its ends
// are listed.
func redactedRunTopology(t Topology, listed, never func(Device) bool, touching bool) dossierTopologyFile {
	hide := map[uuid.UUID]bool{}
	for _, d := range t.Devices {
		if (d.Type == DeviceTypeVPNGateway && IsControlGatewayName(d.Name)) || never(d) {
			hide[d.ID] = true
		}
	}
	// A control switch is a switch or hub cabled to a control gateway.
	for _, c := range t.Connections {
		for _, ep := range c.Endpoints {
			for _, d := range t.Devices {
				if d.ID == ep.DeviceID && d.Type == DeviceTypeVPNGateway && IsControlGatewayName(d.Name) {
					for _, other := range c.Endpoints {
						for _, s := range t.Devices {
							if s.ID == other.DeviceID && s.Type.IsForwarding() {
								hide[s.ID] = true
							}
						}
					}
				}
			}
		}
	}
	byID := make(map[uuid.UUID]Device, len(t.Devices))
	for _, d := range t.Devices {
		byID[d.ID] = d
	}
	isListed := func(ep Endpoint) bool { d, ok := byID[ep.DeviceID]; return ok && !hide[d.ID] && listed(d) }
	isHidden := func(ep Endpoint) bool { return hide[ep.DeviceID] }
	out := dossierTopologyFile{Devices: []dossierDeviceFile{}, Connections: []dossierConnectionFile{}}
	hiddenNIC := map[[2]string]bool{}
	for _, c := range t.Connections {
		keep := !slices.ContainsFunc(c.Endpoints, func(ep Endpoint) bool { return !isListed(ep) })
		if touching {
			keep = slices.ContainsFunc(c.Endpoints, isListed) && !slices.ContainsFunc(c.Endpoints, isHidden)
		}
		if !keep {
			if slices.ContainsFunc(c.Endpoints, isHidden) {
				for _, ep := range c.Endpoints {
					hiddenNIC[[2]string{ep.DeviceID.String(), ep.Interface}] = true
				}
			}
			continue
		}
		entry := dossierConnectionFile{Kind: c.Kind, Endpoints: []dossierEndpointFile{}}
		for _, ep := range c.Endpoints {
			entry.Endpoints = append(entry.Endpoints, dossierEndpointFile{Device: byID[ep.DeviceID].Name, Interface: ep.Interface})
		}
		out.Connections = append(out.Connections, entry)
	}
	for _, d := range t.Devices {
		if hide[d.ID] || !listed(d) {
			continue
		}
		entry := dossierDeviceFile{Name: d.Name, Type: d.Type, SerialPorts: slices.Clone(d.SerialPorts)}
		for _, iface := range d.Interfaces {
			if !hiddenNIC[[2]string{d.ID.String(), iface.Name}] {
				entry.Interfaces = append(entry.Interfaces, dossierInterfaceFile{Name: iface.Name, IP: iface.IP})
			}
		}
		for _, p := range d.ConsolePorts {
			entry.ConsolePorts = append(entry.ConsolePorts, p.Name)
		}
		out.Devices = append(out.Devices, entry)
	}
	return out
}

// WithDossierFiles is a copy of the run's topology in which the tools device has the files of the dossier in its bootstrap
// (after its own). The input is not changed.
func (t Topology) WithDossierFiles(tools Device, d Dossier, platform map[uuid.UUID]bool) (Topology, error) {
	files, err := d.Files(t, tools, platform)
	if err != nil {
		return Topology{}, err
	}
	out := t
	out.Devices = slices.Clone(t.Devices)
	for i := range out.Devices {
		if out.Devices[i].ID == tools.ID {
			out.Devices[i].Bootstrap.Files = append(slices.Clone(out.Devices[i].Bootstrap.Files), files...)
		}
	}
	return out, nil
}
