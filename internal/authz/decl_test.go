package authz

import (
	"strings"
	"testing"
)

func TestDeclStringRoundTrip(t *testing.T) {
	cases := []struct {
		decl Decl
		want string
	}{
		{AnyOf(SourcePlatform, UsersList), "users.list @ platform"},
		{AnyOf(SourceEventParam("id"), EventRead, EventSettingsRead), "anyOf(event.read,event.settings.read) @ event(:id)"},
		{AllOf(SourcePlatform, AccessGrantsCreate, AccessGrantsDelete), "allOf(access.grants.create,access.grants.delete) @ platform"},
		{OwnData(), "self @ self"},
		{PublicRoute(""), "- @ public"},
		{PublicRoute(PublicPages), "- @ public(pages)"},
		{PublicRoute(PublicScoreboard, PublicResults), "- @ public(scoreboard,results)"},
		{ServiceRoute(ServiceRender), "- @ service(render)"},
		{BearerRoute(BearerLiveScreen), "- @ bearer(live-screen)"},
		{Related(SourceOwnTeam("id"), RelTeamMember), "(team-member) @ own-team(:id)"},
		{AnyOf(SourceEventParam("id"), EventParticipantsList).WithData(EventPersonalDataRead), "event.participants.list @ event(:id) data(event.personal-data.read)"},
		{PublicRoute(PublicScoreboard, PublicResults).WithData(EventScoreboardRead), "- @ public(scoreboard,results) data(event.scoreboard.read)"},
		{Related(SourceTenant, RelParticipant), "(participant) @ tenant"},
		{AnyOf(SourceEventParam("id"), EventLabsModeratorTeamUse).Or(RelParticipant), "anyOf(event.labs.moderator-team.use,(participant)) @ event(:id)"},
		{AnyOf(SourceObject("exercise"), ExercisesUpdate, EventExercisesUpdate).Or(RelAuthor).And(ExercisesTestlabStart),
			"allOf(exercises.testlab.start) + anyOf(exercises.update,event.exercises.update,(author)) @ object(exercise)"},
	}
	for _, c := range cases {
		if got := c.decl.String(); got != c.want {
			t.Errorf("String() = %q, want %q", got, c.want)
			continue
		}
		parsed, err := ParseDecl(c.want)
		if err != nil {
			t.Errorf("ParseDecl(%q): %v", c.want, err)
			continue
		}
		if parsed.String() != c.want {
			t.Errorf("ParseDecl(%q).String() = %q", c.want, parsed.String())
		}
	}
}

func TestParseDeclRejectsMalformed(t *testing.T) {
	for _, s := range []string{"", "users.list", "users.list @ ", "users.list @ galaxy", "anyOf(users.list @ platform", "(boss) @ tenant", "event(:) @ platform", "- @ public(gallery)", "- @ public(pages,pages)", "- @ public()", "- @ service(mailer)", "- @ service()", "- @ bearer(webhook)", "self @ self data()", "self @ self data(users.list", "(captain) @ own-team(id)"} {
		if _, err := ParseDecl(s); err == nil {
			t.Errorf("ParseDecl(%q) accepted a malformed declaration", s)
		}
	}
}

func TestDeclProblems(t *testing.T) {
	ok := []Decl{
		AnyOf(SourcePlatform, UsersList),
		AnyOf(SourceEventParam("id"), EventsRead),            // a platform key holds in every event
		AnyOf(SourceEventParam("id"), EventParticipantsList), // an event key at its event
		AnyOf(SourcePlatform, EventsRead, EventAttemptsRead), // event.attempts.read is platform-grantable
		OwnData(),
		PublicRoute(""),
		PublicRoute(PublicScoreboard),
		PublicRoute(PublicScoreboard, PublicResults),
		AnyOf(SourcePublic(""), BannersView),
		Related(SourceTenant, RelParticipant),
		AnyOf(SourceTenant, Self),
		ServiceRoute(ServiceRender),
		BearerRoute(BearerLiveScreen),
		Related(SourceOwnTeam("id"), RelCaptain),
		AnyOf(SourceOwnTeam("id"), Self),
		AnyOf(SourcePlatform, EventsList).WithData(EventsRead, EventsArchivedRead),
	}
	for _, d := range ok {
		if p := d.Problems(); len(p) != 0 {
			t.Errorf("%s: unexpected problems %v", d, p)
		}
	}
	bad := map[string]Decl{
		"not in the catalogue":              AnyOf(SourcePlatform, Key("users.teleport")),
		"wildcard":                          AnyOf(SourcePlatform, All),
		"event key, not platform-grantable": AnyOf(SourcePlatform, EventExercisesCreate),
		"implicit key at platform":          AnyOf(SourcePlatform, Self),
		"platform key at self":              AnyOf(SourceSelf, UsersList),
		"grantable key on a public route":   AnyOf(SourcePublic("pages"), EventPagesRead),
		"no scope":                          {AnyOf: []Key{UsersList}},
		"nothing declared":                  {Scope: SourcePlatform},
		"relation on a public route":        Related(SourcePublic(""), RelParticipant),
		"unknown relation":                  Related(SourceTenant, Relation("boss")),
		"key on a service route":            AnyOf(SourceService(ServiceRender), EventsRead),
		"relation on a service route":       Related(SourceService(ServiceRender), RelParticipant),
		"key on a bearer route":             AnyOf(SourceBearer(BearerLiveScreen), EventsRead),
		"data key not in the catalogue":     AnyOf(SourcePlatform, UsersList).WithData(Key("users.teleport")),
		"data key named twice":              AnyOf(SourcePlatform, UsersList).WithData(UsersRead, UsersRead),
		"wildcard data key":                 AnyOf(SourcePlatform, UsersList).WithData(All),
		"duplicate key":                     AnyOf(SourcePlatform, UsersList, UsersList),
	}
	for name, d := range bad {
		if p := d.Problems(); len(p) == 0 {
			t.Errorf("%s (%s): no problem reported", name, d)
		}
	}
}

func TestDeclKeys(t *testing.T) {
	d := AnyOf(SourcePlatform, UsersList, UsersRead).And(UsersInvite)
	if got := strings.Join(keyStrings(d.Keys()), ","); got != "users.invite,users.list,users.read" {
		t.Errorf("Keys() = %s", got)
	}
}

func keyStrings(keys []Key) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = string(k)
	}
	return out
}

func TestScopeSourceParts(t *testing.T) {
	if got := SourceEventParam("eventID").Param(); got != "eventID" {
		t.Errorf("Param() = %q", got)
	}
	if got := SourcePlatform.Param(); got != "" {
		t.Errorf("platform Param() = %q", got)
	}
	if got := strings.Join(SourcePublic(PublicScoreboard, PublicResults).PublicObjects(), ","); got != "scoreboard,results" {
		t.Errorf("PublicObjects() = %q", got)
	}
	if got := SourcePublic().PublicObjects(); got != nil {
		t.Errorf("bare public PublicObjects() = %v", got)
	}
}

func TestOwnTeamSourceNamesItsEventParameter(t *testing.T) {
	if got := SourceOwnTeam("id").Param(); got != "id" {
		t.Errorf("own-team Param() = %q", got)
	}
	if got := SourceOwnTeam("id").Kind(); got != "own-team" {
		t.Errorf("own-team Kind() = %q", got)
	}
}
