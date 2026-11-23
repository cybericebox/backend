package authz

import (
	"regexp"
	"strings"
)

// keyNamePattern is a dotted lower-case key name: no wildcard, no empty segment.
var keyNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)*$`)

// Covers reports whether a held key or pattern covers a required one (spec §3, plan P1a): equal, or held is "*", or
// held is a dotted prefix ending in ".*" (wildcards work at any level: "event.flags.*", "event.*"). Participants hold
// no keys, so "*" and "event.*" cover every key with no exception (round 3). A pattern also covers a narrower pattern
// ("event.*" covers "event.flags.*"), which the delegation rule needs. There is no bare-prefix rule ("users" covering
// "users.read" in today's rbac) and no "write covers read".
func (held Key) Covers(required Key) bool {
	if held == "" || required == "" {
		return false
	}
	if held == All || held == required {
		return true
	}
	if prefix, ok := strings.CutSuffix(string(held), "*"); ok && strings.HasSuffix(prefix, ".") {
		return strings.HasPrefix(string(required), prefix)
	}
	return false
}

// IsPattern reports whether k is a wildcard ("*" or "x.*").
func IsPattern(k Key) bool { return k == All || strings.HasSuffix(string(k), ".*") }

// Expand returns the catalogue keys a key or pattern covers, in catalogue order.
func Expand(k Key) []Key {
	var out []Key
	for _, s := range catalogue {
		if k.Covers(s.Key) {
			out = append(out, s.Key)
		}
	}
	return out
}

// ValidPattern reports whether k may appear in a role: a catalogue key, "*", or "<prefix>.*" where the prefix is a
// dotted name that is not itself a key and covers at least one key.
func ValidPattern(k Key) bool {
	if k == All {
		return true
	}
	if _, ok := Lookup(k); ok {
		return true
	}
	prefix, ok := strings.CutSuffix(string(k), ".*")
	if !ok || !keyNamePattern.MatchString(prefix) {
		return false
	}
	if _, isKey := Lookup(Key(prefix)); isKey {
		return false
	}
	return len(Expand(k)) > 0
}
