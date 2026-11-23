package exerciseModel

import (
	"fmt"
	"path"
	"slices"

	"github.com/gofrs/uuid"
)

// The defender block of a variant (document 1.1, console design 9a.1) and the dossier built from it (9a.2). The defender, a
// human at the seat or a bot, gets full operating information, like a privileged operator's runbook: which serial line leads to
// which console and what runs there, which devices are off-limits, the operator credentials and the topology. An attacker gets
// none of it.

// DefenderTopology says whether the dossier shows the topology.
type DefenderTopology string

const (
	// DefenderTopologyFull (the default, also when empty): the defender sees the topology.
	DefenderTopologyFull DefenderTopology = "full"
	// DefenderTopologyNone: the dossier has no topology.
	DefenderTopologyNone DefenderTopology = "none"
)

// Limits of the defender block.
const (
	MaxDefenderOffLimits      = 64
	MaxDefenderCredentials    = 64
	MaxDefenderCredentialSize = MaxEnvValueBytes
)

type (
	// Defender is the defender block of a variant.
	Defender struct {
		// Seat is the device the human defender sits at: a node with serial ports; its console cables are the links of the
		// human's dossier. Optional.
		Seat string `json:"seat,omitempty"`
		// Topology is full (default) or none.
		Topology DefenderTopology `json:"topology,omitempty"`
		// OffLimits are devices the defender knows of and must not console into (never a device cabled to the seat).
		OffLimits []string `json:"off_limits,omitempty"`
		// Credentials are the operator credentials, shown to the defender only.
		Credentials []DefenderCredential `json:"credentials,omitempty"`
	}

	// DefenderCredential is one operator credential: a device, optionally one of its console ports, a user and a value. A
	// secret value is stored encrypted, like a secret env value (a blank secret on a save keeps the stored one).
	DefenderCredential struct {
		Device string `json:"device"`
		Port   string `json:"port,omitempty"`
		User   string `json:"user,omitempty"`
		Value  string `json:"value"` // ciphertext at rest when Secret
		Secret bool   `json:"secret,omitempty"`
	}
)

// ShowsTopology says whether the dossier of the block shows the topology; no block is the default (full).
func (d *Defender) ShowsTopology() bool { return d == nil || d.Topology != DefenderTopologyNone }

// CopyDefender is a deep copy (nil stays nil).
func CopyDefender(d *Defender) *Defender {
	if d == nil {
		return nil
	}
	out := *d
	out.OffLimits = slices.Clone(d.OffLimits)
	out.Credentials = slices.Clone(d.Credentials)
	return &out
}

// SealVariant is the variant whose identity a secret credential of the variant is sealed to: the base for a linked variant
// (it takes its base's block as it is), else the variant itself.
func (v Variant) SealVariant() uuid.UUID {
	if v.IsLinked() {
		return v.LinkedTo.UUID
	}
	return v.ID
}

func defenderInvalid(field, reason string) error {
	return ErrDefenderInvalid.WithPublicContext("field", field).WithPublicContext("reason", reason).Err()
}

// ValidateDefender checks the defender block against the variant: the seat is a node with serial ports, the topology value is
// known, every off-limits entry names a device once and never one cabled to the seat, and every credential names a node (and,
// when it names a port, one of its console ports), a user of the account pattern and a value within the size limit. Fields are
// named relative to the variant with 1-based positions. A variant without the block is valid.
func (v Variant) ValidateDefender() error {
	d := v.Defender
	if d == nil {
		return nil
	}
	byName := make(map[string]Device, len(v.Topology.Devices))
	for _, device := range v.Topology.Devices {
		byName[device.Name] = device
	}
	cabled := map[string]bool{}
	if d.Seat != "" {
		seat, ok := byName[d.Seat]
		switch {
		case !ok || !seat.Type.IsNode():
			return defenderInvalid("defender.seat", "seat_unknown")
		case len(seat.SerialPorts) == 0:
			return defenderInvalid("defender.seat", "seat_no_serial")
		}
		for _, link := range v.Topology.ConsoleLinksOf(seat) {
			cabled[link.Device] = true
		}
	}
	if d.Topology != "" && d.Topology != DefenderTopologyFull && d.Topology != DefenderTopologyNone {
		return defenderInvalid("defender.topology", "topology")
	}
	if len(d.OffLimits) > MaxDefenderOffLimits {
		return defenderInvalid("defender.off_limits", "too_many")
	}
	seen := make(map[string]bool, len(d.OffLimits))
	for i, name := range d.OffLimits {
		field := fmt.Sprintf("defender.off_limits[%d]", i+1)
		switch _, ok := byName[name]; {
		case !ok:
			return defenderInvalid(field, "off_limits_unknown")
		case seen[name]:
			return defenderInvalid(field, "off_limits_repeated")
		case cabled[name]:
			return defenderInvalid(field, "off_limits_cabled")
		}
		seen[name] = true
	}
	if len(d.Credentials) > MaxDefenderCredentials {
		return defenderInvalid("defender.credentials", "too_many")
	}
	for i, c := range d.Credentials {
		field := fmt.Sprintf("defender.credentials[%d]", i+1)
		device, ok := byName[c.Device]
		if !ok || !device.Type.IsNode() {
			return defenderInvalid(field+".device", "credential_unknown")
		}
		if c.Port != "" && device.PortOf(c.Port) != PortConsole {
			return defenderInvalid(field+".port", "credential_unknown")
		}
		if c.User != "" && !accountNameRe.MatchString(c.User) {
			return defenderInvalid(field+".user", "credential_user")
		}
		if len(c.Value) > MaxDefenderCredentialSize {
			return defenderInvalid(field+".value", "credential_value")
		}
	}
	return nil
}

// ConsoleKind says what is on a console line, so the defender knows how to drive it.
type ConsoleKind string

const (
	// ConsoleKindLogin: the default /bin/login (a login prompt).
	ConsoleKindLogin ConsoleKind = "login"
	// ConsoleKindProgram: the author's command (a shell or a CLI such as vtysh); the link names its base name.
	ConsoleKindProgram ConsoleKind = "program"
	// ConsoleKindNative: an emulator's or a VM's own console (its own prompts, boot output possible).
	ConsoleKindNative ConsoleKind = "native"
)

// ConsoleLink is one cabled line of a device with serial ports: its serial port and the character device inside, and the device
// and console port at the other end with what runs there.
type ConsoleLink struct {
	SerialPort  string
	Path        string
	Device      string
	ConsolePort string
	Kind        ConsoleKind
	// Program is the base name of the console command, set for ConsoleKindProgram only.
	Program string
}

// kindOf says what runs on a console port.
func kindOf(p ConsolePort) (ConsoleKind, string) {
	switch {
	case p.Native != nil:
		return ConsoleKindNative, ""
	case len(p.Command) == 0 || p.Command[0] == DefaultConsoleCommand:
		return ConsoleKindLogin, ""
	}
	return ConsoleKindProgram, path.Base(p.Command[0])
}

// ConsoleLinksOf are the console cables of the device that end on one of its serial ports, in the order of its serial ports.
func (t Topology) ConsoleLinksOf(device Device) []ConsoleLink {
	byID := make(map[uuid.UUID]Device, len(t.Devices))
	for _, d := range t.Devices {
		byID[d.ID] = d
	}
	var out []ConsoleLink
	for _, serial := range device.SerialPorts {
		for _, c := range t.Connections {
			if !c.IsConsole() || len(c.Endpoints) != 2 {
				continue
			}
			mine := slices.IndexFunc(c.Endpoints, func(ep Endpoint) bool { return ep.DeviceID == device.ID && ep.Interface == serial.Name })
			if mine < 0 {
				continue
			}
			far := c.Endpoints[1-mine]
			peer, ok := byID[far.DeviceID]
			if !ok {
				continue
			}
			at := slices.IndexFunc(peer.ConsolePorts, func(p ConsolePort) bool { return p.Name == far.Interface })
			if at < 0 {
				continue
			}
			kind, program := kindOf(peer.ConsolePorts[at])
			out = append(out, ConsoleLink{
				SerialPort: serial.Name, Path: serial.DevicePath(), Device: peer.Name, ConsolePort: far.Interface, Kind: kind, Program: program,
			})
		}
	}
	return out
}

// Dossier is what a defender is told about the lab it defends (9a.2). Credentials carry plaintext values: a dossier is built
// for one reader at the moment it is shown or written, never stored.
type Dossier struct {
	Links       []ConsoleLink
	Forbidden   []string
	Credentials []DefenderCredential
	// ShowsTopology: the defender sees the topology (the human's schema view, the bot's topology.json).
	ShowsTopology bool
}

// HumanDossier is the dossier of the human defender of the variant as authored: the console cables of the seat, the
// off-limits devices and the credentials (their values as given: the caller opens the secret ones first). The variant must
// declare the block.
func HumanDossier(v Variant, credentials []DefenderCredential) Dossier {
	out := Dossier{ShowsTopology: v.Defender.ShowsTopology(), Credentials: nonNil(credentials), Links: []ConsoleLink{}, Forbidden: []string{}}
	if v.Defender != nil {
		out.Forbidden = append(out.Forbidden, v.Defender.OffLimits...)
		for _, d := range v.Topology.Devices {
			if d.Name == v.Defender.Seat && d.Type.IsNode() {
				out.Links = nonNil(v.Topology.ConsoleLinksOf(d))
			}
		}
	}
	return out
}

// BotDossier is the dossier of a bot defender's tools device in the run's topology (after auto-cabling, so it is the real
// cabling): the console cables of the tools device, the off-limits devices plus every device with a console port it does not
// operate, and the credentials. defender may be nil (the variant declares no block).
func BotDossier(run Topology, tools Device, operates []string, defender *Defender, credentials []DefenderCredential) Dossier {
	out := Dossier{Links: nonNil(run.ConsoleLinksOf(tools)), ShowsTopology: defender.ShowsTopology(), Credentials: nonNil(credentials), Forbidden: []string{}}
	seen := map[string]bool{}
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			out.Forbidden = append(out.Forbidden, name)
		}
	}
	if defender != nil {
		for _, name := range defender.OffLimits {
			add(name)
		}
	}
	for _, d := range run.Devices {
		if d.ID != tools.ID && len(d.ConsolePorts) > 0 && !slices.Contains(operates, d.Name) {
			add(d.Name)
		}
	}
	return out
}

// nonNil keeps an empty list empty but not nil, so the files and the API write [] rather than null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
