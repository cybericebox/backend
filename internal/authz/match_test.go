package authz

import "testing"

func TestCovers(t *testing.T) {
	cases := []struct {
		held, required Key
		want           bool
	}{
		{All, EventFlagsRead, true},
		{All, UsersDelete, true},
		{All, Self, true},
		{EventFlagsRead, EventFlagsRead, true},
		{"event.*", EventFlagsRead, true},
		{"event.*", EventStatsSensitiveRead, true},
		{"event.stats.*", EventStatsSensitiveRead, true},
		{"event.stats.*", EventStatsRead, true},
		{"event.stats.sensitive.*", EventStatsSensitiveUpdate, true},
		{"event.stats.*", EventScoreboardRead, false},
		{"event.*", EventsList, false}, // "event." is not a prefix of "events."
		{"events.*", EventRead, false},
		{"events.*", EventsArchivedUpdate, true},
		{"exercises.*", ExercisesTestlabStart, true},
		{"users.*", UsersStatusUpdate, true},
		{"event.*", "event.*", true},
		{"event.*", "event.flags.*", true},
		{"event.flags.*", "event.*", false},
		{EventFlagsRead, "event.*", false},
		{EventRead, EventReadSuffixed, false},
		{"event", EventRead, false}, // a bare prefix is no wildcard (unlike today's rbac "users")
		{EventsRead, EventsArchivedRead, false},
		{"event.flags.read.*", EventFlagsRead, false},
		{"", EventRead, false},
		{EventRead, "", false},
	}
	for _, c := range cases {
		if got := c.held.Covers(c.required); got != c.want {
			t.Errorf("%q covers %q = %v, want %v", c.held, c.required, got, c.want)
		}
	}
}

// EventReadSuffixed shares the text prefix of event.read without being below it.
const EventReadSuffixed Key = "event.readers"

func TestValidPattern(t *testing.T) {
	cases := []struct {
		pattern Key
		want    bool
	}{
		{All, true},
		{EventRead, true},
		{"event.*", true},
		{"event.stats.*", true},
		{"exercises.*", true},
		{"nothing.*", false},          // covers no key
		{"event.*.read", false},       // a wildcard only as the last segment
		{"event*", false},             // not a segment
		{"event.flags.read.*", false}, // no key below a key
		{"events.write", false},       // a legacy permission
		{"", false},
		{"Event.read", false},
	}
	for _, c := range cases {
		if got := ValidPattern(c.pattern); got != c.want {
			t.Errorf("ValidPattern(%q) = %v, want %v", c.pattern, got, c.want)
		}
	}
}

func TestExpand(t *testing.T) {
	if got := Expand("event.stats.*"); len(got) != 4 {
		t.Errorf("event.stats.* expands to %v, want the four event.stats keys", got)
	}
	if got := Expand(All); len(got) != len(Catalogue()) {
		t.Errorf("* expands to %d keys, want every key", len(got))
	}
	if got := Expand(EventRead); len(got) != 1 || got[0] != EventRead {
		t.Errorf("a plain key expands to itself, got %v", got)
	}
	if got := Expand("nothing.*"); len(got) != 0 {
		t.Errorf("an unknown pattern expands to nothing, got %v", got)
	}
}
