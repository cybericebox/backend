package exerciseModel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"unicode/utf8"

	"github.com/gofrs/uuid"
)

// Opponents are an optional block of a variant of the exercise document (cib-exercise/1.0): the automated attackers or
// defenders the Academy may run against the lab of the variant. They are author content for the Academy only: an event
// ignores them, and no participant-facing view carries them. The author says who the opponent is and what it aims at;
// the platform decides the image, the toolset, the keys and the budget, so none of those can be set here.

// OpponentRole is what an opponent does in the lab.
type OpponentRole string

const (
	OpponentAttack        OpponentRole = "attack"
	OpponentDefense       OpponentRole = "defense"
	OpponentAttackDefense OpponentRole = "attack-defense"
)

// Valid says whether the role is one of the three.
func (r OpponentRole) Valid() bool {
	return r == OpponentAttack || r == OpponentDefense || r == OpponentAttackDefense
}

// Operates says whether the role works on devices of the lab (defense and attack-defense do; an attacker only reaches
// the lab from outside).
func (r OpponentRole) Operates() bool { return r == OpponentDefense || r == OpponentAttackDefense }

const (
	// OpponentBandMin and OpponentBandMax bound the level of an opponent.
	OpponentBandMin = 1
	OpponentBandMax = 6
	// MaxOpponents is how many opponents one variant may have.
	MaxOpponents = 8
	// opponentBriefMaxLen caps the brief like the description of an exercise (in characters).
	opponentBriefMaxLen = descriptionMaxLen
	// opponentStartImmediate is the written form of an opponent that starts with the lab.
	opponentStartImmediate = "immediate"
)

// OpponentStart says when an opponent starts: with the lab (After empty), or after the task with that key is done.
type OpponentStart struct {
	After string
}

// Immediate says the opponent starts with the lab.
func (s OpponentStart) Immediate() bool { return s.After == "" }

// MarshalJSON writes "immediate" or {"after": "<task key>"}.
func (s OpponentStart) MarshalJSON() ([]byte, error) {
	if s.Immediate() {
		return json.Marshal(opponentStartImmediate)
	}
	return json.Marshal(struct {
		After string `json:"after"`
	}{After: s.After})
}

// UnmarshalJSON reads "immediate" or {"after": "<task key>"}; anything else (another word, another key, a number) is an
// error. A missing start is the zero value, which is immediate.
func (s *OpponentStart) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		*s = OpponentStart{}
		return nil
	}
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var word string
		if err := json.Unmarshal(trimmed, &word); err != nil || word != opponentStartImmediate {
			return fmt.Errorf("start must be %q or an object with after", opponentStartImmediate)
		}
		*s = OpponentStart{}
		return nil
	}
	var object struct {
		After string `json:"after"`
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&object); err != nil || object.After == "" {
		return fmt.Errorf("start must be %q or an object with after", opponentStartImmediate)
	}
	*s = OpponentStart{After: object.After}
	return nil
}

// Opponent is one automated attacker or defender of a variant. Goal and Start name tasks of the same variant by key;
// Operates names devices of its topology.
type Opponent struct {
	Role OpponentRole `json:"role"`
	Band int          `json:"band"`
	// Goal is the ordered list of the task keys (the success checks, flags included) the opponent aims at.
	Goal  []string      `json:"goal"`
	Start OpponentStart `json:"start"`
	// Operates is required for defense and attack-defense and forbidden for attack.
	Operates []string `json:"operates,omitempty"`
	// Foothold is the name of a switch or hub of the variant's topology where the opponent's tools device is cabled in: its
	// first position in the lab. Required for attack and attack-defense, forbidden for defense (a defender sits with the
	// devices it operates).
	Foothold string `json:"foothold,omitempty"`
	// Brief is the author's text for the opponent (author content, not UI text).
	Brief string `json:"brief,omitempty"`
	// Seat (defense and attack-defense only, document 1.1) is the device a human defender of the task sits at: a node with
	// serial ports. A run of the opponent takes that seat (the deploy and the auto-cabling do it, a later phase); here it is
	// only checked.
	Seat string `json:"seat,omitempty" cib:"1.1"`
}

// The reasons ErrOpponentInvalid names in its public context, next to the field.
const (
	OpponentReasonCount            = "too_many"
	OpponentReasonRole             = "role"
	OpponentReasonBand             = "band"
	OpponentReasonGoalEmpty        = "goal_empty"
	OpponentReasonGoalUnknown      = "goal_unknown"
	OpponentReasonGoalRepeated     = "goal_repeated"
	OpponentReasonStartUnknown     = "start_unknown"
	OpponentReasonOperatesNeeded   = "operates_required"
	OpponentReasonOperatesForbid   = "operates_forbidden"
	OpponentReasonOperatesUnknown  = "operates_unknown"
	OpponentReasonOperatesRepeated = "operates_repeated"
	OpponentReasonFootholdNeeded   = "foothold_required"
	OpponentReasonFootholdForbid   = "foothold_forbidden"
	OpponentReasonFootholdUnknown  = "foothold_unknown"
	OpponentReasonBriefTooLong     = "brief_too_long"
	OpponentReasonForbiddenField   = "forbidden_field"
	// The seat of an opponent (opponents[N].seat).
	OpponentReasonSeatUnknown    = "seat_unknown"
	OpponentReasonSeatNoSerial   = "seat_no_serial"
	OpponentReasonSeatForbidden  = "seat_forbidden"
	OpponentReasonSeatRepeated   = "seat_repeated"
	OpponentReasonSeatOperated   = "seat_operated"
	OpponentReasonSeatOnFoothold = "seat_on_foothold"
	OpponentReasonSeatNoNetwork  = "seat_no_network"
	OpponentReasonSeatReferenced = "seat_referenced"
	// OpponentReasonSeatConsoleEnd: the seat is the console end of a cable (another device's serial port is cabled to a console
	// port of it). A seat is only a serial-port connector, what a human defender sits at.
	OpponentReasonSeatConsoleEnd = "seat_console_end"
)

func opponentInvalid(field, reason string) error {
	return ErrOpponentInvalid.WithPublicContext("field", field).WithPublicContext("reason", reason).Err()
}

// ValidateOpponents checks the opponents of the variant against the variant itself: the goal and the start name tasks
// that exist (by key), and operates names devices of the topology. Errors name the field relative to the variant, with
// 1-based positions as the rest of the document does: opponents[2].operates[1]. A variant without opponents is valid.
func (v Variant) ValidateOpponents() error {
	if len(v.Opponents) == 0 {
		return nil
	}
	if len(v.Opponents) > MaxOpponents {
		return opponentInvalid("opponents", OpponentReasonCount)
	}
	taskKeys := make(map[string]bool, len(v.Tasks))
	for _, task := range v.Tasks {
		if task.Key != "" {
			taskKeys[task.Key] = true
		}
	}
	deviceNames := make(map[string]bool, len(v.Topology.Devices))
	segments := make(map[string]bool)
	for _, device := range v.Topology.Devices {
		deviceNames[device.Name] = true
		if device.Type.IsForwarding() {
			segments[device.Name] = true
		}
	}
	for i, opponent := range v.Opponents {
		at := fmt.Sprintf("opponents[%d]", i+1)
		if !opponent.Role.Valid() {
			return opponentInvalid(at+".role", OpponentReasonRole)
		}
		if opponent.Band < OpponentBandMin || opponent.Band > OpponentBandMax {
			return opponentInvalid(at+".band", OpponentReasonBand)
		}
		if len(opponent.Goal) == 0 {
			return opponentInvalid(at+".goal", OpponentReasonGoalEmpty)
		}
		seenGoal := make(map[string]bool, len(opponent.Goal))
		for gi, ref := range opponent.Goal {
			switch {
			case !taskKeys[ref]:
				return opponentInvalid(fmt.Sprintf("%s.goal[%d]", at, gi+1), OpponentReasonGoalUnknown)
			case seenGoal[ref]:
				return opponentInvalid(fmt.Sprintf("%s.goal[%d]", at, gi+1), OpponentReasonGoalRepeated)
			}
			seenGoal[ref] = true
		}
		if !opponent.Start.Immediate() && !taskKeys[opponent.Start.After] {
			return opponentInvalid(at+".start.after", OpponentReasonStartUnknown)
		}
		if err := validateOperates(at, opponent, deviceNames); err != nil {
			return err
		}
		switch {
		case opponent.Role == OpponentDefense && opponent.Foothold != "":
			return opponentInvalid(at+".foothold", OpponentReasonFootholdForbid)
		case opponent.Role != OpponentDefense && opponent.Foothold == "":
			return opponentInvalid(at+".foothold", OpponentReasonFootholdNeeded)
		case opponent.Foothold != "" && !segments[opponent.Foothold]:
			return opponentInvalid(at+".foothold", OpponentReasonFootholdUnknown)
		}
		if utf8.RuneCountInString(opponent.Brief) > opponentBriefMaxLen {
			return opponentInvalid(at+".brief", OpponentReasonBriefTooLong)
		}
	}
	return v.validateSeats()
}

// validateSeats checks the seats of the opponents (console design, section 8.1): only a role that operates has one; it names a
// node of the variant with serial ports, held by one opponent at most, that no opponent operates; it has a data NIC
// with a network connection (where a bot run puts its tools device), and the first such NIC is on the foothold segment of
// no opponent; and no lab check, address reference or route names it (a bot run does not deploy the seat).
func (v Variant) validateSeats() error {
	byName := make(map[string]Device, len(v.Topology.Devices))
	byID := make(map[uuid.UUID]Device, len(v.Topology.Devices))
	for _, device := range v.Topology.Devices {
		byName[device.Name] = device
		byID[device.ID] = device
	}
	var footholds []Device
	operated := make(map[string]bool)
	for _, opponent := range v.Opponents {
		for _, name := range opponent.Operates {
			operated[name] = true
		}
		if segment, ok := byName[opponent.Foothold]; ok && segment.Type.IsForwarding() {
			footholds = append(footholds, segment)
		}
	}
	taken := make(map[string]bool)
	for i, opponent := range v.Opponents {
		if opponent.Seat == "" {
			continue
		}
		field := fmt.Sprintf("opponents[%d].seat", i+1)
		seat, known := byName[opponent.Seat]
		switch {
		case !opponent.Role.Operates():
			return opponentInvalid(field, OpponentReasonSeatForbidden)
		case !known || !seat.Type.IsNode():
			return opponentInvalid(field, OpponentReasonSeatUnknown)
		case len(seat.SerialPorts) == 0:
			return opponentInvalid(field, OpponentReasonSeatNoSerial)
		case v.Topology.isConsoleEnd(seat):
			return opponentInvalid(field, OpponentReasonSeatConsoleEnd)
		case taken[opponent.Seat]:
			return opponentInvalid(field, OpponentReasonSeatRepeated)
		case operated[opponent.Seat]: // by this opponent or another one: a seat is not a device a bot defends
			return opponentInvalid(field, OpponentReasonSeatOperated)
		}
		taken[opponent.Seat] = true
		nic, ok := v.Topology.firstDataNIC(seat)
		if !ok {
			return opponentInvalid(field, OpponentReasonSeatNoNetwork)
		}
		if v.Topology.onAnySegment(byID, seat, nic, footholds) {
			return opponentInvalid(field, OpponentReasonSeatOnFoothold)
		}
		if v.referencesSeat(seat) {
			return opponentInvalid(field, OpponentReasonSeatReferenced)
		}
	}
	return nil
}

// isConsoleEnd reports whether a console cable ends on a console port of the device.
func (t Topology) isConsoleEnd(d Device) bool {
	for _, c := range t.Connections {
		if !c.IsConsole() {
			continue
		}
		for _, ep := range c.Endpoints {
			if ep.Kind == EndpointDevice && ep.DeviceID == d.ID && d.PortOf(ep.Interface) == PortConsole {
				return true
			}
		}
	}
	return false
}

// firstDataNIC is the first interface of the device, in its order, that has a network connection.
func (t Topology) firstDataNIC(d Device) (string, bool) {
	for _, iface := range d.Interfaces {
		for _, c := range t.Connections {
			if c.IsConsole() {
				continue
			}
			for _, ep := range c.Endpoints {
				if ep.Kind == EndpointDevice && ep.DeviceID == d.ID && ep.Interface == iface.Name {
					return iface.Name, true
				}
			}
		}
	}
	return "", false
}

// onAnySegment reports whether the interface of the device is in the broadcast domain of one of the switches or hubs (cabled
// to it directly or through other switches and hubs).
func (t Topology) onAnySegment(byID map[uuid.UUID]Device, d Device, iface string, segments []Device) bool {
	if len(segments) == 0 {
		return false
	}
	links, vertex := t.l2Links(byID)
	at := vertex(Endpoint{Kind: EndpointDevice, DeviceID: d.ID, Interface: iface})
	for _, segment := range segments {
		if reachable(links, vertex(Endpoint{Kind: EndpointDevice, DeviceID: segment.ID}))[at] {
			return true
		}
	}
	return false
}

// referencesSeat reports whether something of the variant names the device: a condition of a lab check observes it, or an
// interface of another device uses one of its addresses as a gateway or a route's next hop (a literal address or the address
// reference the seat holds).
func (v Variant) referencesSeat(seat Device) bool {
	for _, task := range v.Tasks {
		if task.Check.State == nil {
			continue
		}
		for _, condition := range task.Check.State.Conditions {
			if condition.DeviceID == seat.ID {
				return true
			}
		}
	}
	addresses := map[string]bool{}
	refs := map[NetworkIPRef]bool{}
	for _, iface := range seat.Interfaces {
		for _, address := range iface.IP.Addresses {
			if prefix, err := netip.ParsePrefix(address); err == nil {
				addresses[prefix.Addr().String()] = true
			}
		}
		if iface.IP.AddressRef != nil {
			refs[*iface.IP.AddressRef] = true
		}
	}
	hits := func(literal string, ref *NetworkIPRef) bool {
		if literal != "" {
			if parsed, err := netip.ParseAddr(literal); err == nil && addresses[parsed.String()] {
				return true
			}
		}
		return ref != nil && refs[*ref]
	}
	for _, d := range v.Topology.Devices {
		if d.ID == seat.ID {
			continue
		}
		for _, iface := range d.Interfaces {
			if hits(iface.IP.Gateway, iface.IP.GatewayRef) {
				return true
			}
			for _, route := range iface.IP.Routes {
				if hits(route.Via, route.ViaRef) {
					return true
				}
			}
		}
	}
	return false
}

func validateOperates(at string, opponent Opponent, deviceNames map[string]bool) error {
	if !opponent.Role.Operates() {
		if len(opponent.Operates) > 0 {
			return opponentInvalid(at+".operates", OpponentReasonOperatesForbid)
		}
		return nil
	}
	if len(opponent.Operates) == 0 {
		return opponentInvalid(at+".operates", OpponentReasonOperatesNeeded)
	}
	seen := make(map[string]bool, len(opponent.Operates))
	for oi, name := range opponent.Operates {
		field := fmt.Sprintf("%s.operates[%d]", at, oi+1)
		switch {
		case !deviceNames[name]:
			return opponentInvalid(field, OpponentReasonOperatesUnknown)
		case seen[name]:
			return opponentInvalid(field, OpponentReasonOperatesRepeated)
		}
		seen[name] = true
	}
	return nil
}

// WithoutOpponents is the variant without its opponents: what a reader who is not the author may see.
func (v Variant) WithoutOpponents() Variant {
	v.Opponents = nil
	return v
}

// CopyOpponents is a deep copy of the list, so a copy of a variant never shares it.
func CopyOpponents(opponents []Opponent) []Opponent {
	if opponents == nil {
		return nil
	}
	out := make([]Opponent, len(opponents))
	for i, o := range opponents {
		o.Goal = slices.Clone(o.Goal)
		o.Operates = slices.Clone(o.Operates)
		out[i] = o
	}
	return out
}
