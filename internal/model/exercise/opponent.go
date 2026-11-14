package exerciseModel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"unicode/utf8"
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
	// Brief is the author's text for the opponent (author content, not UI text).
	Brief string `json:"brief,omitempty"`
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
	OpponentReasonBriefTooLong     = "brief_too_long"
	OpponentReasonForbiddenField   = "forbidden_field"
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
	for _, device := range v.Topology.Devices {
		deviceNames[device.Name] = true
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
		if utf8.RuneCountInString(opponent.Brief) > opponentBriefMaxLen {
			return opponentInvalid(at+".brief", OpponentReasonBriefTooLong)
		}
	}
	return nil
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
