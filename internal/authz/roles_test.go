package authz

import (
	"slices"
	"testing"
)

// wantRoleKeys is every built-in role of spec §4.4 / Appendix A.2 expanded to its catalogue keys (with the owner's
// decisions of 2026-11-23: catalog_manager manages the whole catalog; admin_viewer and admin hold at platform scope
// exactly the platform-grantable event keys today's events.read / events.write / exercises.read /
// events.solution-attempts.read reach; admin archives only through events.archive).
var wantRoleKeys = map[RoleName][]Key{
	// super_admin holds "*": every catalogue key (checked separately).
	"admin": {
		"users.list",
		"users.read",
		"users.invite",
		"users.status.update",
		"users.delete",
		"access.grants.read",
		"access.grants.create",
		"access.grants.delete",
		"access.audit.read",
		"platform.audit.read",
		"events.list",
		"events.read",
		"events.create",
		"events.settings.update",
		"events.staff.update",
		"events.staff.delete",
		"events.owner.transfer",
		"events.stats_source.update",
		"events.archive",
		"events.archived.read",
		"events.archived.update",
		"events.delete",
		"exercises.list",
		"exercises.read",
		"exercises.all.read",
		"exercises.create",
		"exercises.update",
		"exercises.delete",
		"exercises.publish",
		"exercises.flags.read",
		"exercises.export",
		"exercises.secrets.read",
		"exercises.audiences.update",
		"exercises.link",
		"exercises.testlab.start",
		"event.read",
		"event.participants.list",
		"event.participants.read",
		"event.personal-data.read",
		"event.tasks.list",
		"event.tasks.read",
		"event.exercises.list",
		"event.exercises.read",
		"event.flags.read",
		"event.attempts.read",
		"event.attempts.answers.read",
		"event.scoreboard.read",
		"event.stats.sensitive.read",
		"event.stats.people.read",
		"event.archived.read",
		// Reached today through events.read (RequireRead, the analytics sections) and events.write /
		// events.solution-attempts.write (the admin event routes, requireExport, the integrity reviews).
		"event.settings.read",
		"event.pages.read",
		"event.teams.read",
		"event.forms.read",
		"event.labs.read",
		"event.registry.images.read",
		"event.comms.read",
		"event.stats.read",
		"event.participants.update",
		"event.participants.delete",
		"event.attempts.update",
		"event.export",
		"event.stats.sensitive.update",
	},
	"admin_viewer": {
		"users.list",
		"users.read",
		"events.list",
		"events.read",
		"events.archived.read",
		"exercises.list",
		"exercises.read",
		"exercises.all.read",
		"exercises.flags.read",
		"event.read",
		"event.participants.list",
		"event.participants.read",
		"event.personal-data.read",
		"event.tasks.list",
		"event.tasks.read",
		"event.exercises.list",
		"event.exercises.read",
		"event.flags.read",
		"event.attempts.read",
		"event.attempts.answers.read",
		"event.scoreboard.read",
		"event.stats.people.read",
		"event.archived.read",
		// Reached today through events.read (RequireRead, the analytics sections).
		"event.settings.read",
		"event.pages.read",
		"event.teams.read",
		"event.forms.read",
		"event.labs.read",
		"event.registry.images.read",
		"event.comms.read",
		"event.stats.read",
	},
	"user": {
		"self",
		"notifications.self",
	},
	"public": {
		"banners.view",
	},
	"catalog_author": {
		"exercises.create",
	},
	"catalog_reader": {
		"exercises.list",
		"exercises.read",
	},
	"catalog_editor": {
		"exercises.list",
		"exercises.read",
		"exercises.update",
		"exercises.publish",
		"exercises.flags.read",
		"exercises.export",
	},
	"catalog_manager": {
		"events.list",
		"exercises.list",
		"exercises.read",
		"exercises.all.read",
		"exercises.create",
		"exercises.update",
		"exercises.delete",
		"exercises.publish",
		"exercises.flags.read",
		"exercises.export",
		"exercises.secrets.read",
		"exercises.audiences.update",
		"exercises.link",
		"exercises.testlab.start",
	},
	"sysadmin": {
		"platform.settings.read",
		"platform.settings.update",
		"platform.settings.delete",
		"platform.errors.read",
		"platform.errors.update",
		"infrastructure.read",
		"infrastructure.update",
		"infrastructure.delete",
		"exercises.elevations.read",
		"exercises.elevations.update",
	},
	"cyberbrain_user": {
		"cyberbrain.chat.use",
		"cyberbrain.sessions.read",
		"cyberbrain.runs.start",
		"cyberbrain.runs.read",
	},
	"event_owner": {
		"exercises.testlab.start",
		"event.read",
		"event.settings.read",
		"event.settings.update",
		"event.pages.read",
		"event.pages.update",
		"event.pages.delete",
		"event.participants.list",
		"event.participants.read",
		"event.participants.update",
		"event.participants.delete",
		"event.personal-data.read",
		"event.teams.read",
		"event.teams.update",
		"event.teams.delete",
		"event.forms.read",
		"event.forms.update",
		"event.tasks.list",
		"event.tasks.read",
		"event.tasks.update",
		"event.tasks.delete",
		"event.tasks.export",
		"event.exercises.list",
		"event.exercises.read",
		"event.exercises.create",
		"event.exercises.update",
		"event.exercises.delete",
		"event.exercises.publish",
		"event.flags.read",
		"event.attempts.read",
		"event.attempts.answers.read",
		"event.attempts.update",
		"event.scoreboard.read",
		"event.scoreboard.update",
		"event.labs.read",
		"event.labs.update",
		"event.labs.moderator-team.use",
		"event.registry.images.read",
		"event.registry.read",
		"event.registry.update",
		"event.registry.delete",
		"event.comms.read",
		"event.comms.update",
		"event.comms.delete",
		"event.stats.read",
		"event.stats.sensitive.read",
		"event.stats.sensitive.update",
		"event.stats.people.read",
		"event.export",
		"event.staff.read",
		"event.staff.update",
		"event.staff.delete",
		"event.owner.transfer",
		"event.preview.participant",
		"event.access.audit.read",
		"event.exercises.request",
		"event.archived.read",
	},
	"event_manager": {
		"exercises.testlab.start",
		"event.read",
		"event.settings.read",
		"event.settings.update",
		"event.pages.read",
		"event.pages.update",
		"event.pages.delete",
		"event.participants.list",
		"event.participants.read",
		"event.participants.update",
		"event.participants.delete",
		"event.personal-data.read",
		"event.teams.read",
		"event.teams.update",
		"event.teams.delete",
		"event.forms.read",
		"event.forms.update",
		"event.tasks.list",
		"event.tasks.read",
		"event.tasks.update",
		"event.tasks.delete",
		"event.tasks.export",
		"event.exercises.list",
		"event.exercises.read",
		"event.exercises.create",
		"event.exercises.update",
		"event.exercises.delete",
		"event.exercises.publish",
		"event.flags.read",
		"event.attempts.read",
		"event.attempts.answers.read",
		"event.attempts.update",
		"event.scoreboard.read",
		"event.scoreboard.update",
		"event.labs.read",
		"event.labs.update",
		"event.labs.moderator-team.use",
		"event.registry.images.read",
		"event.registry.read",
		"event.registry.update",
		"event.registry.delete",
		"event.comms.read",
		"event.comms.update",
		"event.comms.delete",
		"event.stats.read",
		"event.stats.sensitive.read",
		"event.stats.sensitive.update",
		"event.stats.people.read",
		"event.export",
		"event.staff.read",
		"event.staff.update",
		"event.staff.delete",
		"event.preview.participant",
		"event.access.audit.read",
		"event.exercises.request",
		"event.archived.read",
	},
	"event_moderator": {
		"exercises.testlab.start",
		"event.read",
		"event.settings.read",
		"event.settings.update",
		"event.pages.read",
		"event.pages.update",
		"event.pages.delete",
		"event.participants.list",
		"event.participants.read",
		"event.participants.update",
		"event.participants.delete",
		"event.personal-data.read",
		"event.teams.read",
		"event.teams.update",
		"event.teams.delete",
		"event.forms.read",
		"event.forms.update",
		"event.tasks.list",
		"event.tasks.read",
		"event.tasks.update",
		"event.tasks.delete",
		"event.tasks.export",
		"event.exercises.list",
		"event.exercises.read",
		"event.exercises.create",
		"event.exercises.update",
		"event.exercises.delete",
		"event.exercises.publish",
		"event.flags.read",
		"event.attempts.read",
		"event.attempts.answers.read",
		"event.attempts.update",
		"event.scoreboard.read",
		"event.scoreboard.update",
		"event.labs.read",
		"event.labs.update",
		"event.labs.moderator-team.use",
		"event.registry.images.read",
		"event.registry.read",
		"event.registry.update",
		"event.registry.delete",
		"event.comms.read",
		"event.comms.update",
		"event.comms.delete",
		"event.stats.read",
		"event.stats.sensitive.read",
		"event.stats.sensitive.update",
		"event.stats.people.read",
		"event.export",
		"event.staff.read",
		"event.preview.participant",
		"event.archived.read",
	},
	"event_observer": {
		"event.read",
		"event.settings.read",
		"event.pages.read",
		"event.participants.list",
		"event.participants.read",
		"event.teams.read",
		"event.forms.read",
		"event.tasks.list",
		"event.tasks.read",
		"event.attempts.read",
		"event.scoreboard.read",
		"event.labs.read",
		"event.registry.images.read",
		"event.comms.read",
		"event.stats.read",
		"event.staff.read",
		"event.archived.read",
	},
	"event_task_author": {
		"exercises.testlab.start",
		"event.read",
		"event.tasks.list",
		"event.tasks.read",
		"event.tasks.update",
		"event.tasks.delete",
		"event.exercises.list",
		"event.exercises.read",
		"event.exercises.create",
		"event.exercises.update",
		"event.registry.images.read",
	},
	"event_analyst": {
		"event.read",
		"event.stats.read",
		"event.archived.read",
	},
	"event_content": {
		"event.read",
		"event.settings.read",
		"event.settings.update",
		"event.pages.read",
		"event.pages.update",
		"event.pages.delete",
		"event.forms.read",
		"event.forms.update",
	},
	"event_teams": {
		"event.read",
		"event.participants.list",
		"event.participants.read",
		"event.participants.update",
		"event.participants.delete",
		"event.personal-data.read",
		"event.teams.read",
		"event.teams.update",
		"event.teams.delete",
	},
	"event_lab_operator": {
		"event.read",
		"event.tasks.list",
		"event.tasks.read",
		"event.labs.read",
		"event.labs.update",
		"event.labs.moderator-team.use",
		"event.registry.images.read",
	},
	"event_communications": {
		"event.read",
		"event.pages.read",
		"event.pages.update",
		"event.pages.delete",
		"event.comms.read",
		"event.comms.update",
		"event.comms.delete",
	},
}

func TestBuiltinRolesHoldTheirKeys(t *testing.T) {
	roles := BuiltinRoles()
	if len(roles) != len(wantRoleKeys)+1 {
		t.Errorf("%d built-in roles, want %d", len(roles), len(wantRoleKeys)+1)
	}
	for _, role := range roles {
		got := role.Expanded()
		if role.Name == SuperAdmin {
			if !slices.Equal(role.Keys, []Key{All}) || len(got) != len(Catalogue()) {
				t.Errorf("super_admin: keys %v, want [*] covering every key", role.Keys)
			}
			continue
		}
		want, ok := wantRoleKeys[role.Name]
		if !ok {
			t.Errorf("%s: not a role of §4.4", role.Name)
			continue
		}
		for _, k := range want {
			if !slices.Contains(got, k) {
				t.Errorf("%s: missing %s", role.Name, k)
			}
		}
		for _, k := range got {
			if !slices.Contains(want, k) {
				t.Errorf("%s: holds %s, which §4.4 / A.2 do not give it", role.Name, k)
			}
		}
	}
}

func TestBuiltinRolesUseOnlyCatalogueKeys(t *testing.T) {
	seen := map[RoleName]bool{}
	for _, role := range BuiltinRoles() {
		if seen[role.Name] {
			t.Errorf("%s: defined twice", role.Name)
		}
		seen[role.Name] = true
		if role.Description == "" {
			t.Errorf("%s: no description", role.Name)
		}
		for _, k := range role.Keys {
			if !ValidPattern(k) {
				t.Errorf("%s: %q is neither a catalogue key nor a wildcard over catalogue keys", role.Name, k)
			}
			if k == All && role.Name != SuperAdmin {
				t.Errorf("%s: only super_admin holds *", role.Name)
			}
		}
	}
}

// A key in a role that its grant's scope type does not allow does nothing there (§3); a built-in role holds no such
// dead key. A platform role holds an event key only when it is platform-grantable (§5.2 rule 2).
func TestBuiltinRoleKeysAreEffectiveAtTheRoleScope(t *testing.T) {
	for _, role := range BuiltinRoles() {
		if role.Name == SuperAdmin {
			continue
		}
		for _, k := range role.Expanded() {
			spec, _ := Lookup(k)
			effective := false
			for _, s := range spec.Scopes {
				if role.EffectiveAt(k, s) {
					effective = true
				}
			}
			if !effective {
				t.Errorf("%s (granted at %s): %s is effective nowhere", role.Name, role.Scope, k)
			}
		}
	}
}

func TestRoleEffectiveAt(t *testing.T) {
	admin, _ := BuiltinRole(Admin)
	viewer, _ := BuiltinRole(AdminViewer)
	owner, _ := BuiltinRole(EventOwner)
	super, _ := BuiltinRole(SuperAdmin)
	cases := []struct {
		role  Role
		key   Key
		scope ScopeType
		want  bool
	}{
		{admin, EventsList, ScopePlatform, true},
		{admin, EventsList, ScopeEvent, false},        // a platform key acts at platform only
		{admin, EventFlagsRead, ScopeEvent, true},     // platform-grantable: in every event
		{admin, EventFlagsRead, ScopePlatform, false}, // an event key never acts at platform
		{admin, EventStatsSensitiveRead, ScopeEvent, true},
		{viewer, EventStatsSensitiveRead, ScopeEvent, false},
		{admin, EventArchive, ScopeEvent, false},     // admin archives through events.archive
		{admin, EventSettingsRead, ScopeEvent, true}, // reached today through events.read
		{admin, EventPagesUpdate, ScopeEvent, false}, // not platform-grantable // not platform-grantable
		{owner, EventSettingsRead, ScopeEvent, true},
		{owner, EventArchive, ScopeEvent, false},
		{owner, ExercisesTestlabStart, ScopeEvent, true},
		{owner, ExercisesTestlabStart, ScopePlatform, false},
		{super, EventPagesUpdate, ScopeEvent, true}, // a grant holding * reaches every event (§5.2 rule 2)
		{super, EventPagesUpdate, ScopePlatform, false},
		{super, UsersDelete, ScopePlatform, true},
		{super, Self, ScopeImplicit, true},
		{super, "events.write", ScopePlatform, false}, // not a key
	}
	for _, c := range cases {
		if got := c.role.EffectiveAt(c.key, c.scope); got != c.want {
			t.Errorf("%s: %s at %s = %v, want %v", c.role.Name, c.key, c.scope, got, c.want)
		}
	}
}

func TestBuiltinRoleReturnsACopy(t *testing.T) {
	role, ok := BuiltinRole(Admin)
	if !ok {
		t.Fatal("admin is a built-in role")
	}
	role.Keys[0] = "changed"
	again, _ := BuiltinRole(Admin)
	if again.Keys[0] == "changed" {
		t.Fatal("BuiltinRole exposes its backing array")
	}
	if _, ok := BuiltinRole("event_participant"); ok {
		t.Fatal("participation has no roles (§4.4, round 3)")
	}
}
