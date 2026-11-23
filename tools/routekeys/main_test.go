package main

import (
	"os"
	"strings"
	"testing"
)

func TestConvert(t *testing.T) {
	cases := []struct{ keys, scope, want string }{
		{"`self`", "—", "self @ self"},
		{"`public`", "—", "- @ public"},
		{"`public` (visibility: event pages, §4.5)", "path :id → event", "- @ public(pages)"},
		{"`public` (visibility: scoreboard during the event, results after it, §4.5)", "path :id → event", "- @ public(scoreboard,results)"},
		{"service secret (X-Render-Secret)", "path :tag", "- @ service(render)"},
		{"`users.list`", "none", "users.list @ platform"},
		{"`events.archive`", "platform (bulk: body IDs)", "events.archive @ platform"},
		{"all-of: `access.grants.create`, `access.grants.delete`", "none (delegation §6.4)", "allOf(access.grants.create,access.grants.delete) @ platform"},
		{"any-of: `events.read`, `event.settings.read`", "path :id → event", "anyOf(events.read,event.settings.read) @ event(:id)"},
		{"`events.delete`; an archived event: all-of `events.delete`, `events.archived.read`", "path :id → event (bulk: body IDs)", "events.delete @ event(:id)"},
		{"any-of: `exercises.update`, `event.exercises.update`, (author)", "path :id → exercise (author, audiences, owner event)", "anyOf(exercises.update,event.exercises.update,(author)) @ object(exercise)"},
		{"all-of: `cyberbrain.runs.start` and (`exercises.update`, `event.exercises.update` or author)", "path :id → exercise (author, audiences, owner event)", "allOf(cyberbrain.runs.start) + anyOf(exercises.update,event.exercises.update,(author)) @ object(exercise)"},
		{"`(participant)`", "tenant (Origin) → event", "(participant) @ tenant"},
		{"`(captain)`", "path :teamID → the caller's team (relation)", "(captain) @ object(team)"},
		{"`(team member)`", "own team (relation)", "(team-member) @ own-team(:id)"},
		{"`self`", "own team (lookup)", "self @ own-team(:id)"},
		{"bearer token (live-screen link)", "tenant (Origin) → event", "- @ bearer(live-screen)"},
		{"any-of: `(participant)`, `event.labs.moderator-team.use`", "path :id → event", "anyOf(event.labs.moderator-team.use,(participant)) @ event(:id)"},
	}
	for _, c := range cases {
		d, why := convert(c.keys, c.scope)
		if why != "" {
			t.Errorf("%s @ %s: %s", c.keys, c.scope, why)
			continue
		}
		if got := d.String(); got != c.want {
			t.Errorf("%s @ %s = %s, want %s", c.keys, c.scope, got, c.want)
		}
	}
	for _, c := range [][2]string{
		{"any-of: `exercises.create`, `event.exercises.create`", "none (own upload)"},
		{"`exercises.read`", "own job"},
	} {
		if _, why := convert(c[0], c[1]); why == "" {
			t.Errorf("%s @ %s: converted, want it left open", c[0], c[1])
		}
	}
}

func TestReadAppendixAndOverrides(t *testing.T) {
	spec := strings.Join([]string{
		"#### A.3.1 Outside the API registry",
		"| 1 | `GET /api/health` | none | x | `public` | — |  |  |",
		"#### A.3.2 Sign-in and own account",
		"| 4 | `DELETE /auth/account` | `self` | x | `self` | — |  |  |",
		"| 92 | `GET /events` | `events.read` | x | `events.list` | none | Rows only; archived rows need `events.archived.read` |  |",
		"#### A.3.14 New routes",
		"| N3 | `GET /access/roles` | — | new | `roles.manage` | platform |  |  |",
	}, "\n")
	routes, err := readAppendix(strings.NewReader(spec))
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 3 || routes["GET /api/health"] != nil || routes["GET /api/access/roles"] == nil {
		t.Fatalf("routes = %v", routes)
	}
	if err := applyOverrides(routes, "DELETE /api/auth/account | data: users.delete | test\n"); err != nil {
		t.Fatal(err)
	}
	out := render(routes, "abc123")
	for _, want := range []string{
		"# spec: abc123\n",
		"DELETE /api/auth/account | keys: self @ self | data: users.delete | A.3 #4; override: test",
		"GET    /api/events | keys: events.list @ platform | data: events.archived.read | A.3 #92",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render() lacks %q:\n%s", want, out)
		}
	}
	if err := applyOverrides(routes, "+POST /api/auth/step-up/passkey/begin | keys: self @ self | built with P7, not in A.3 yet\n"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(render(routes, "abc123"), "POST   /api/auth/step-up/passkey/begin | keys: self @ self | data: - | not in A.3; override: built with P7, not in A.3 yet") {
		t.Errorf("an added route is not rendered")
	}
	if err := applyOverrides(routes, "+GET /api/events | keys: events.list @ platform | x\n"); err == nil {
		t.Error("adding a route A.3 lists passed")
	}
	if err := applyOverrides(routes, "GET /api/nowhere | keys: self @ self | x\n"); err == nil {
		t.Error("an override for a route outside A.3 passed")
	}
	if err := applyOverrides(routes, "GET /api/events | keys: users.teleport @ platform | x\n"); err == nil {
		t.Error("an override with an unknown key passed")
	}
}

// The committed routes.expected-keys and overrides.txt name the same spec commit: regenerating the file against a
// newer spec changes its header, and this fails until overrides.txt has been read against that spec and its commit
// line moved with it.
func TestExpectedKeysAndOverridesNameTheSameSpecCommit(t *testing.T) {
	commitOf := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, SpecCommitLine) {
				return strings.TrimPrefix(line, SpecCommitLine)
			}
		}
		t.Fatalf("%s has no %q line", path, SpecCommitLine)
		return ""
	}
	expected, overrides := commitOf("../../"+defaultOut), commitOf("overrides.txt")
	if expected != overrides {
		t.Errorf("routes.expected-keys was generated from spec %s, overrides.txt was read against %s: review the overrides against the new spec and update its commit line", expected, overrides)
	}
	if len(expected) != 40 {
		t.Errorf("spec commit %q is not a full SHA", expected)
	}
}
