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

// Files are the dossier as the bootstrap files of the tools device: consoles.json and credentials.json always, topology.json
// when the dossier shows the topology (the run's topology, redacted). Each is 0600, owned by root.
func (d Dossier) Files(run Topology) ([]BootstrapFile, error) {
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
	if d.ShowsTopology {
		contents = append(contents, struct {
			path string
			v    any
		}{DossierTopologyPath, redactedRunTopology(run)})
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

// redactedRunTopology is the shape of the run's topology without what a device runs and without the platform's control.
func redactedRunTopology(t Topology) dossierTopologyFile {
	control := map[uuid.UUID]bool{}
	for _, d := range t.Devices {
		if d.Type == DeviceTypeVPNGateway && IsControlGatewayName(d.Name) {
			control[d.ID] = true
		}
	}
	// A control switch is a switch or hub cabled to a control gateway.
	for _, c := range t.Connections {
		if slices.ContainsFunc(c.Endpoints, func(ep Endpoint) bool { return control[ep.DeviceID] }) {
			for _, ep := range c.Endpoints {
				for _, d := range t.Devices {
					if d.ID == ep.DeviceID && d.Type.IsForwarding() {
						control[d.ID] = true
					}
				}
			}
		}
	}
	names := make(map[uuid.UUID]string, len(t.Devices))
	for _, d := range t.Devices {
		names[d.ID] = d.Name
	}
	out := dossierTopologyFile{Devices: []dossierDeviceFile{}, Connections: []dossierConnectionFile{}}
	controlNIC := map[[2]string]bool{}
	for _, c := range t.Connections {
		if !slices.ContainsFunc(c.Endpoints, func(ep Endpoint) bool { return control[ep.DeviceID] }) {
			entry := dossierConnectionFile{Kind: c.Kind, Endpoints: []dossierEndpointFile{}}
			for _, ep := range c.Endpoints {
				entry.Endpoints = append(entry.Endpoints, dossierEndpointFile{Device: names[ep.DeviceID], Interface: ep.Interface})
			}
			out.Connections = append(out.Connections, entry)
			continue
		}
		for _, ep := range c.Endpoints {
			controlNIC[[2]string{ep.DeviceID.String(), ep.Interface}] = true
		}
	}
	for _, d := range t.Devices {
		if control[d.ID] {
			continue
		}
		entry := dossierDeviceFile{Name: d.Name, Type: d.Type, SerialPorts: slices.Clone(d.SerialPorts)}
		for _, iface := range d.Interfaces {
			if !controlNIC[[2]string{d.ID.String(), iface.Name}] {
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
func (t Topology) WithDossierFiles(tools uuid.UUID, d Dossier) (Topology, error) {
	files, err := d.Files(t)
	if err != nil {
		return Topology{}, err
	}
	out := t
	out.Devices = slices.Clone(t.Devices)
	for i := range out.Devices {
		if out.Devices[i].ID == tools {
			out.Devices[i].Bootstrap.Files = append(slices.Clone(out.Devices[i].Bootstrap.Files), files...)
		}
	}
	return out, nil
}
