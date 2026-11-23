package authz

import (
	"slices"
	"testing"

	platformSettingsModel "github.com/cybericebox/backend/internal/model/platformSettings"
)

// The catalogue tests of the access-control spec (platform/specs/access-control-design.md §10, Appendix A.2).

func TestCatalogueHasTheKeysOfAppendixA2(t *testing.T) {
	keys := Catalogue()
	if len(keys) != 128 {
		t.Fatalf("catalogue has %d keys, Appendix A.2 has 128", len(keys))
	}
	seen := map[Key]bool{}
	counts := map[string]int{}
	for _, spec := range keys {
		if seen[spec.Key] {
			t.Errorf("%s: listed twice", spec.Key)
		}
		seen[spec.Key] = true
		if !keyNamePattern.MatchString(string(spec.Key)) {
			t.Errorf("%s: not a dotted lower-case key name (wildcards belong to roles, not to the catalogue)", spec.Key)
		}
		switch {
		case spec.Allows(ScopePlatform):
			counts["platform"]++
		case spec.Allows(ScopeEvent):
			counts["event"]++
		case spec.Allows(ScopePublic):
			counts["public"]++
		case spec.Allows(ScopeImplicit):
			counts["implicit"]++
		}
	}
	want := map[string]int{"platform": 68, "event": 57, "public": 1, "implicit": 2}
	for kind, n := range want {
		if counts[kind] != n {
			t.Errorf("%s keys: got %d, want %d (A.1)", kind, counts[kind], n)
		}
	}
}

func TestEveryKeyHasADescriptionAndAScopeType(t *testing.T) {
	known := []ScopeType{ScopePlatform, ScopeEvent, ScopeImplicit, ScopePublic}
	for _, spec := range Catalogue() {
		if spec.Description == "" {
			t.Errorf("%s: no description", spec.Key)
		}
		if len(spec.Scopes) == 0 {
			t.Errorf("%s: no allowed scope type", spec.Key)
		}
		for _, s := range spec.Scopes {
			if !slices.Contains(known, s) {
				t.Errorf("%s: unknown scope type %q", spec.Key, s)
			}
		}
		if (spec.Allows(ScopeImplicit) || spec.Allows(ScopePublic)) && len(spec.Scopes) != 1 {
			t.Errorf("%s: an implicit or public key has no other scope type", spec.Key)
		}
		if spec.Grantable() != (spec.Allows(ScopePlatform) || spec.Allows(ScopeEvent)) {
			t.Errorf("%s: Grantable disagrees with the scope types", spec.Key)
		}
	}
}

func TestKeyFlagsFollowAppendixA2(t *testing.T) {
	platformGrantable := []Key{"event.read", "event.settings.read", "event.pages.read", "event.teams.read", "event.forms.read", "event.labs.read", "event.registry.images.read", "event.comms.read", "event.stats.read", "event.participants.update", "event.participants.delete", "event.attempts.update", "event.export", "event.stats.sensitive.update", "event.participants.list", "event.participants.read", "event.personal-data.read", "event.tasks.list", "event.tasks.read", "event.exercises.list", "event.exercises.read", "event.flags.read", "event.attempts.read", "event.attempts.answers.read", "event.scoreboard.read", "event.stats.sensitive.read", "event.stats.people.read", "event.archive", "event.archived.read"}
	survivesEventEnd := []Key{"event.read", "event.scoreboard.read", "event.stats.read", "event.export", "event.staff.read", "event.access.audit.read", "event.archived.read"}
	secondFactor := []Key{"roles.manage", "access.groups.manage", "access.grants.read", "access.grants.create", "access.grants.delete", "access.audit.read", "events.staff.update", "events.staff.delete", "event.staff.update", "event.staff.delete"}
	for _, spec := range Catalogue() {
		if got, want := spec.PlatformGrantable, slices.Contains(platformGrantable, spec.Key); got != want {
			t.Errorf("%s: PlatformGrantable = %v, want %v", spec.Key, got, want)
		}
		if got, want := spec.SurvivesEventEnd, slices.Contains(survivesEventEnd, spec.Key); got != want {
			t.Errorf("%s: SurvivesEventEnd = %v, want %v", spec.Key, got, want)
		}
		if got, want := spec.SecondFactor, slices.Contains(secondFactor, spec.Key); got != want {
			t.Errorf("%s: SecondFactor = %v, want %v", spec.Key, got, want)
		}
		// Only an event key can be platform-grantable ("in every event", §9 Q20), and only an event key outlives its event.
		if spec.PlatformGrantable && (spec.Allows(ScopePlatform) || !spec.Allows(ScopeEvent)) {
			t.Errorf("%s: platform-grantable but not an event-only key", spec.Key)
		}
		if spec.SurvivesEventEnd && !spec.Allows(ScopeEvent) {
			t.Errorf("%s: survives the event end but is not an event key", spec.Key)
		}
		if !spec.Grantable() && (spec.PlatformGrantable || spec.SurvivesEventEnd || spec.SecondFactor) {
			t.Errorf("%s: an implicit or public key carries no flag", spec.Key)
		}
	}
}

func TestLookup(t *testing.T) {
	spec, ok := Lookup(EventFlagsRead)
	if !ok || spec.Key != EventFlagsRead || !spec.PlatformGrantable {
		t.Fatalf("Lookup(%s) = %+v, %v", EventFlagsRead, spec, ok)
	}
	if _, ok := Lookup("events.write"); ok {
		t.Fatal("events.write is a legacy permission, not a key")
	}
	if _, ok := Lookup(All); ok {
		t.Fatal("the wildcard is not a catalogue key")
	}
	// Catalogue hands out a copy.
	keys := Catalogue()
	keys[0].Description = "changed"
	if again, _ := Lookup(keys[0].Key); again.Description == "changed" {
		t.Fatal("Catalogue exposes its backing array")
	}
}

// §7.2: app_settings.required_permission keeps its strings, validated against the catalogue. A value is either a
// catalogue key or the legacy permission of one (platform.settings.write: platform.settings.update/delete).
func TestEverySettingRequiredPermissionIsKnown(t *testing.T) {
	for _, value := range platformSettingsModel.ReadPermissions {
		p := platformSettingsModel.PlatformSetting{RequiredPermission: value}.ReadPermission()
		if _, ok := Lookup(Key(p)); ok {
			continue
		}
		if len(KeysForLegacyPermission(p)) == 0 {
			t.Errorf("required_permission %q (read as %q) is neither a key nor the legacy permission of one", value, p)
		}
	}
}
