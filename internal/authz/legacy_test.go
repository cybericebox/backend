package authz

import (
	"slices"
	"testing"

	eventManagerModel "github.com/cybericebox/backend/internal/model/eventManager"
	"github.com/cybericebox/backend/internal/model/rbac"
)

func TestEveryKeyMapsToALegacyCheck(t *testing.T) {
	for _, spec := range Catalogue() {
		l, ok := LegacyOf(spec.Key)
		if !ok {
			t.Errorf("%s: no legacy mapping", spec.Key)
			continue
		}
		if len(l.Permissions) == 0 && l.Check == "" {
			t.Errorf("%s: neither a legacy permission nor a named legacy check", spec.Key)
		}
		for _, p := range l.Permissions {
			if p == "" || p == rbac.PermAll {
				t.Errorf("%s: legacy permission %q", spec.Key, p)
			}
		}
		if l.Check != "" && !slices.Contains(LegacyChecks(), l.Check) {
			t.Errorf("%s: unknown legacy check %q", spec.Key, l.Check)
		}
	}
	for k := range legacy {
		if _, ok := Lookup(k); !ok {
			t.Errorf("legacy mapping for %q, which is not a catalogue key", k)
		}
	}
}

// today's platform roles and the built-in role that replaces each at the switch (§7.1). public is everyone's.
var platformMigration = map[rbac.Role]RoleName{
	rbac.RoleSuperAdmin:  SuperAdmin,
	rbac.RoleAdmin:       Admin,
	rbac.RoleAdminViewer: AdminViewer,
	rbac.RoleUser:        User,
	rbac.RolePublic:      Public,
}

// The changes made on purpose at the platform roles (spec §7.2, owner decisions): role, key → why.
var intendedPlatformChanges = map[RoleName]map[Key]string{
	Admin: {
		EventsStatsSourceUpdate: "gained: the per-event statistics checkbox (§7.2, Q14)",
		CyberbrainChatUse:       "lost: Cyber Brain is explicit only, nobody but * at the switch (§7.2, Q1)",
		CyberbrainSessionsRead:  "lost: Cyber Brain is explicit only, nobody but * at the switch (§7.2, Q1)",
		CyberbrainRunsStart:     "lost: grader and opponent runs need cyberbrain.runs.* besides task write (§7.2, Q12)",
		CyberbrainRunsRead:      "lost: grader and opponent runs need cyberbrain.runs.* besides task write (§7.2, Q12)",
	},
	AdminViewer: {
		ExercisesPublish: "lost: the proposal queue is read with exercises.publish (§7.2, round 3)",
	},
}

// newRoleHolds: the key is effective for the built-in role at one of the key's scope types, or for the implicit user
// role of every signed-in account (today's roles inherit user), or for the public role of everyone; or a platform
// key the role holds carries today's reach on the same routes (Legacy.CarriedBy).
func newRoleHolds(name RoleName, k Key) bool {
	spec, _ := Lookup(k)
	l, _ := LegacyOf(k)
	roles := []RoleName{name, Public}
	if name != Public {
		roles = append(roles, User)
	}
	for _, n := range roles {
		role, _ := BuiltinRole(n)
		for _, s := range spec.Scopes {
			if role.EffectiveAt(k, s) {
				return true
			}
		}
		if n == name && slices.ContainsFunc(l.CarriedBy, role.Covers) {
			return true
		}
	}
	return false
}

// At the switch nobody loses access (owner rule): for every key whose legacy is a platform permission, the built-in
// role holds the key (or the platform key that carries it) exactly when today's role holds that permission, except the
// changes made on purpose listed above. Keys decided only by a named check are compared at the event roles below and
// by the route parity test (P1b, P2).
func TestPlatformRolesKeepTodaysReach(t *testing.T) {
	for old, name := range platformMigration {
		for _, spec := range Catalogue() {
			l, _ := LegacyOf(spec.Key)
			if len(l.Permissions) == 0 {
				continue
			}
			today := slices.ContainsFunc(l.Permissions, old.HasPermission)
			after := newRoleHolds(name, spec.Key)
			_, listed := intendedPlatformChanges[name][spec.Key]
			switch {
			case today == after && listed:
				t.Errorf("%s / %s: listed as an intended change but nothing changes", name, spec.Key)
			case today != after && !listed:
				t.Errorf("%s / %s: today %v (legacy %v), after the switch %v", name, spec.Key, today, l.Permissions, after)
			}
		}
	}
}

func TestCarriedByNamesPlatformKeys(t *testing.T) {
	for _, spec := range Catalogue() {
		l, _ := LegacyOf(spec.Key)
		for _, c := range l.CarriedBy {
			carrier, ok := Lookup(c)
			if !ok || !carrier.Allows(ScopePlatform) {
				t.Errorf("%s: carried by %q, which is not a platform key", spec.Key, c)
			}
		}
	}
}

// today's event roles and the built-in event role that replaces each at the switch (§7.1 backfill).
var eventMigration = map[eventManagerModel.Role]RoleName{
	eventManagerModel.RoleOwner:   EventOwner,
	eventManagerModel.RoleManager: EventModerator,
	eventManagerModel.RoleViewer:  EventObserver,
}

// The changes made on purpose at the event roles (spec §7.2, §9): role, key → why.
var intendedEventChanges = map[RoleName]map[Key]string{
	EventOwner: {
		EventStaffRead:        "gained: staff managed from the event side (Q4)",
		EventStaffUpdate:      "gained: staff managed from the event side (Q4)",
		EventStaffDelete:      "gained: staff managed from the event side (Q4)",
		EventOwnerTransfer:    "gained: ownership transfer (Q5)",
		EventAccessAuditRead:  "gained: the event's grant journal (Q15)",
		EventExercisesRequest: "gained: access requests (Q18)",
	},
	EventModerator: {
		EventStaffRead: "gained: the staff list (Q4)",
	},
	EventObserver: {
		EventStaffRead:     "gained: the staff list (Q4)",
		EventExercisesList: "lost: observers stop seeing the task library (decision 12)",
		EventExercisesRead: "lost: observers stop seeing the task library (decision 12)",
	},
}

// For every key that an event grant can carry, the built-in event role holds it exactly when today's event role
// passes the key's legacy check, except the changes listed above.
func TestEventRolesKeepTodaysReach(t *testing.T) {
	for old, name := range eventMigration {
		role, _ := BuiltinRole(name)
		for _, spec := range Catalogue() {
			if !spec.Allows(ScopeEvent) {
				continue
			}
			l, _ := LegacyOf(spec.Key)
			today := slices.Contains(l.Check.EventRoles(), old)
			after := role.EffectiveAt(spec.Key, ScopeEvent)
			_, listed := intendedEventChanges[name][spec.Key]
			switch {
			case today == after && listed:
				t.Errorf("%s / %s: listed as an intended change but nothing changes", name, spec.Key)
			case today != after && !listed:
				t.Errorf("%s / %s: today %v (%s), after the switch %v", name, spec.Key, today, l.Check, after)
			}
		}
	}
}

func TestKeysForLegacyPermission(t *testing.T) {
	got := KeysForLegacyPermission(string(rbac.PermPlatformSettingsWrite))
	for _, k := range []Key{PlatformSettingsUpdate, PlatformSettingsDelete} {
		if !slices.Contains(got, k) {
			t.Errorf("platform.settings.write: %v lacks %s", got, k)
		}
	}
	if got := KeysForLegacyPermission("billing.read"); len(got) != 0 {
		t.Errorf("an unknown permission maps to %v", got)
	}
}
