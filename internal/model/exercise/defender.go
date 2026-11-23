package exerciseModel

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/gofrs/uuid"
)

// The defender block of a variant (document 1.1, console design 9a.1) and the dossier built from it (9a.2). The defender, a
// human at the seat or a bot, gets full operating information, like a privileged operator's runbook: which serial line leads to
// which console and what runs there, which devices are off-limits, the operator credentials and the topology. An attacker gets
// none of it.

// DefenderTopology says whether the dossier shows the topology.
type DefenderTopology string

// The values are a closed enum (confirmed with the brain session, which holds the exercisedoc).
const (
	// DefenderTopologyFull (the default, also when empty): the defender sees the whole topology, redacted.
	DefenderTopologyFull DefenderTopology = "full"
	// DefenderTopologyOperates: the defender sees only its own devices (its seat or tools device and what it operates) and
	// their links.
	DefenderTopologyOperates DefenderTopology = "operates"
	// DefenderTopologyNone: the dossier has no topology.
	DefenderTopologyNone DefenderTopology = "none"
)

// Valid says whether the value is one of the enum (empty is the default, full).
func (t DefenderTopology) Valid() bool {
	return t == "" || t == DefenderTopologyFull || t == DefenderTopologyOperates || t == DefenderTopologyNone
}

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
		// Topology is full (default), operates or none.
		Topology DefenderTopology `json:"topology,omitempty"`
		// OffLimits are devices the defender knows of and must not console into (never a device cabled to the seat). It is an
		// override: absent (or empty), the forbidden list of the dossier is the complement of what the defender operates
		// (ForbiddenFor).
		OffLimits []string `json:"off_limits,omitempty"`
		// Credentials are the operator credentials, shown to the defender only.
		Credentials []DefenderCredential `json:"credentials,omitempty"`
	}

	// DefenderCredential is one operator credential: a device, optionally one of its console ports, a user and a value. A
	// secret value follows exactly the rules of a secret env value (EnvVar) on every path: entered in the editor and sealed at
	// rest to its variant, a blank save keeps it, a linked variant or a copy takes its source's value sealed under its own id,
	// blank in every read model and every export except a password-protected archive, and an import refuses a non-empty
	// secret value except from a password-protected archive. A credential is known by (device, port, user): two credentials of
	// a variant never share it.
	DefenderCredential struct {
		Device string `json:"device"`
		Port   string `json:"port,omitempty"`
		User   string `json:"user,omitempty"`
		Value  string `json:"value"` // ciphertext at rest when Secret
		Secret bool   `json:"secret,omitempty"`
	}
)

// TopologyMode is the topology the dossier of the block shows; no block, or no value, is the default (full).
func (d *Defender) TopologyMode() DefenderTopology {
	if d == nil || d.Topology == "" {
		return DefenderTopologyFull
	}
	return d.Topology
}

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

func defenderInvalid(field, reason string) error {
	return ErrDefenderInvalid.WithPublicContext("field", field).WithPublicContext("reason", reason).Err()
}

// ValidateDefender checks the defender block against the variant: the seat is a node with serial ports, the topology value is
// known, every off-limits entry names a device once and never one cabled to the seat, and every credential names a node (and,
// when it names a port, one of its console ports), a user of the account pattern and a value within the size limit. Fields are
// named relative to the variant with 1-based positions; two credentials with one (device, port, user) are refused
// (credential_repeated). A variant without the block is valid.
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
	if !d.Topology.Valid() {
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
	identities := make(map[[3]string]bool, len(d.Credentials))
	for i, c := range d.Credentials {
		field := fmt.Sprintf("defender.credentials[%d]", i+1)
		identity := [3]string{c.Device, c.Port, c.User}
		if identities[identity] {
			return defenderInvalid(field, "credential_repeated")
		}
		identities[identity] = true
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
		// A secret value is stored sealed; its length is not the author's text any more (as for a secret env value).
		if !c.Secret && (len(c.Value) > MaxDefenderCredentialSize || strings.ContainsRune(c.Value, 0)) {
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
	SerialPort  string      `json:"serial_port"`
	Path        string      `json:"path"`
	Device      string      `json:"device"`
	ConsolePort string      `json:"console_port"`
	Kind        ConsoleKind `json:"console_kind"`
	// Program is the base name of the console command, set for ConsoleKindProgram only.
	Program string `json:"program"`
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

// Dossier is what a defender is told about the lab it defends (9a.2). Credentials carry their values as the caller put them: a
// dossier of the moment holds plaintext and is never stored; the snapshot of a test lab (DeployedDossier) holds the sealed ones.
type Dossier struct {
	Links       []ConsoleLink        `json:"links"`
	Forbidden   []string             `json:"forbidden"`
	Credentials []DefenderCredential `json:"credentials"`
	// Operates are the devices the defender operates: the bot's opponent.operates, the human's (see HumanOperates).
	Operates []string `json:"operates"`
	// Topology is what the defender sees of the topology: full, operates (Operates and their links) or none.
	Topology DefenderTopology `json:"topology"`
}

// ForbiddenFor is the forbidden list of a dossier, the one rule for the human and the bot (confirmed with the brain session):
// off_limits when the block names any (an override), else the complement of what the defender operates: every node of the
// topology with a console port that it does not operate. self, the defender's own device, is never in it.
func ForbiddenFor(t Topology, offLimits, operates []string, self string) []string {
	if len(offLimits) > 0 {
		return slices.Clone(offLimits)
	}
	out := []string{}
	for _, d := range t.Devices {
		if d.Type.IsNode() && len(d.ConsolePorts) > 0 && d.Name != self && !slices.Contains(operates, d.Name) {
			out = append(out, d.Name)
		}
	}
	return out
}

// HumanOperates is what the human defender of the variant operates: the operates of an opponent with the same seat when there
// is one (a bot run at that seat defends the same devices), else the devices the seat is cabled to.
func HumanOperates(v Variant) []string {
	if v.Defender == nil {
		return []string{}
	}
	for _, o := range v.Opponents {
		if o.Seat != "" && o.Seat == v.Defender.Seat {
			return slices.Clone(o.Operates)
		}
	}
	out := []string{}
	for _, link := range humanLinks(v) {
		if !slices.Contains(out, link.Device) {
			out = append(out, link.Device)
		}
	}
	return out
}

func humanLinks(v Variant) []ConsoleLink {
	if v.Defender == nil {
		return []ConsoleLink{}
	}
	for _, d := range v.Topology.Devices {
		if d.Name == v.Defender.Seat && d.Type.IsNode() {
			return nonNil(v.Topology.ConsoleLinksOf(d))
		}
	}
	return []ConsoleLink{}
}

// HumanDossier is the dossier of the human defender of the variant as authored: the console cables of the seat, the forbidden
// list (ForbiddenFor with HumanOperates), the credentials as given and the topology mode. The variant must declare the block.
func HumanDossier(v Variant, credentials []DefenderCredential) Dossier {
	operates := HumanOperates(v)
	var offLimits []string
	seat := ""
	if v.Defender != nil {
		offLimits, seat = v.Defender.OffLimits, v.Defender.Seat
	}
	return Dossier{
		Links: humanLinks(v), Forbidden: ForbiddenFor(v.Topology, offLimits, operates, seat), Credentials: nonNil(credentials),
		Operates: operates, Topology: v.Defender.TopologyMode(),
	}
}

// BotDossier is the dossier of a bot defender's tools device in the run's topology (after auto-cabling, so it is the real
// cabling): the console cables of the tools device, the forbidden list (ForbiddenFor with the opponent's operates), the
// credentials and the topology mode. defender may be nil (the variant declares no block).
func BotDossier(run Topology, tools Device, operates []string, defender *Defender, credentials []DefenderCredential) Dossier {
	var offLimits []string
	if defender != nil {
		offLimits = defender.OffLimits
	}
	return Dossier{
		Links: nonNil(run.ConsoleLinksOf(tools)), Forbidden: ForbiddenFor(run, offLimits, operates, tools.Name), Credentials: nonNil(credentials),
		Operates: nonNil(slices.Clone(operates)), Topology: defender.TopologyMode(),
	}
}

// DeployedDossier is the human defender's dossier of a test lab as it was deployed, kept with the test deploy: a later edit of
// the draft never changes it. Secret credential values stay sealed as stored (to Variant, the deployed variant) and are opened
// only when the owner reads the dossier.
type DeployedDossier struct {
	Variant uuid.UUID `json:"variant"`
	Dossier
}

// nonNil keeps an empty list empty but not nil, so the files and the API write [] rather than null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
