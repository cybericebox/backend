package main

import (
	"strings"
	"testing"
)

func TestRouteWrappingAGatePasses(t *testing.T) {
	got := parse(t, `package x
func (h *H) Init(r *gin.RouterGroup) {
	self := h.prot.RequirePermission(rbac.PermSelf)
	own := h.prot.Route(authz.OwnData(), self)
	r.GET("users", h.prot.Route(authz.AnyOf(authz.SourcePlatform, authz.UsersList), h.prot.RequirePermission(rbac.PermUsersRead)), h.list)
	r.GET("me", h.prot.Route(authz.OwnData(), self), h.me)
	r.GET("account", own, h.account)
}`)
	if len(got) != 0 {
		t.Fatalf("want 0 violations, got %v", got)
	}
}

// A declaration is not a gate: Route without a legacy gate on a path that is not allowlisted as public still fails.
func TestRouteWithoutGateIsViolation(t *testing.T) {
	got := parse(t, `package x
func (h *H) Init(r *gin.RouterGroup) {
	r.GET("users", h.prot.Route(authz.PublicRoute(), nil), h.list)
}`)
	if len(got) != 1 {
		t.Fatalf("want 1 violation, got %v", got)
	}
}

// Route wraps only the permission gate: any other check is a middleware of its own (a layer of the route).
func TestRouteAroundANonPermissionCheckIsViolation(t *testing.T) {
	got := parse(t, `package x
func (h *H) Init(r *gin.RouterGroup) {
	g := r.Group("x", h.prot.RequirePermission(rbac.PermSelf))
	g.GET("dashboard", h.prot.Route(authz.AnyOf(authz.SourcePlatform, authz.UsersList), h.requireAdminTier), h.get)
}`)
	if len(got) != 1 || !strings.Contains(got[0], "neither a RequirePermission gate nor nil") {
		t.Fatalf("want the Route argument violation, got %v", got)
	}
}

func TestDeclarationViolations(t *testing.T) {
	exceptions := map[string]string{
		"DELETE /api/auth/sessions/:id": "own sessions",
		"DELETE /api/auth/last-email":   "the browser's own cookie",
	}
	golden := strings.Join([]string{
		"GET    /api/users | gate: users.read | layers: - | keys: users.list @ platform",
		"GET    /api/auth/me | gate: self | layers: - | keys: self @ self",
		"POST   /api/auth/sign-in | gate: - | layers: recaptcha | keys: - @ public",
		"GET    /api/events/:id | gate: events.read | layers: - | keys: ?",
		"DELETE /api/users/:userID | gate: users.delete | layers: - | keys: users.delete @ platform",
		"POST   /api/users/bulk/delete | gate: users.delete | layers: - | keys: users.delete @ platform",
		"DELETE /api/auth/sessions/:id | gate: self | layers: - | keys: self @ self",
		"DELETE /api/auth/last-email | gate: - | layers: - | keys: - @ public",
		"GET    /api/admin/assistant/statistics/events/:eventID | gate: platform.settings.read | layers: - | keys: cyberbrain.stats.read @ event(:eventID)",
		"GET    /api/render/events/:tag/page | gate: - | layers: eventself.requireSecret | keys: - @ service(render)",
		"GET    /api/events/self/live-screen | gate: events.content.read(public) | layers: eventself.liveScreenGate,middleware.ResolveEventTenant.func | keys: - @ bearer(live-screen)",
		"GET    /api/events/:id/teams/mine/members | gate: self | layers: eventself.touchPresence | keys: (team-member) @ own-team(:id)",
	}, "\n")
	if got := declarationViolations(golden, exceptions, nil); len(got) != 0 {
		t.Fatalf("want no violations, got %v", got)
	}

	bad := map[string]string{
		"unknown key":           "GET    /api/users | gate: users.read | layers: - | keys: users.teleport @ platform",
		"scope source":          "POST   /api/me/draft-key | gate: self | layers: - | keys: event.exercises.create @ platform",
		"delete without key":    "DELETE /api/users/:userID | gate: users.delete | layers: - | keys: users.status.update @ platform",
		"reject without key":    "POST   /api/events/:id/participants/:userID/reject | gate: events.write | layers: - | keys: event.participants.update @ event(:id)",
		"own-data delete":       "DELETE /api/auth/account | gate: self | layers: - | keys: self @ self",
		"public delete":         "DELETE /api/auth/google/pending | gate: - | layers: - | keys: - @ public",
		"delete key off delete": "PATCH  /api/users/:userID/status | gate: users.status.write | layers: - | keys: users.delete @ platform",
		"bare self in manage":   "GET    /api/events/:id/manage/teams | gate: self | layers: event.requireRead | keys: self @ event(:id)",
		"bare self exercises":   "GET    /api/exercises | gate: self | layers: - | keys: self @ self",
		"bare self assistant":   "GET    /api/assistant/sessions | gate: self | layers: - | keys: self @ self",
		"unreadable":            "GET    /api/users | gate: users.read | layers: - | keys: users.list",
		"no keys column":        "GET    /api/users | gate: users.read | layers: -",
		"event param missing":   "POST   /api/events/bulk/archive | gate: events.write | layers: - | keys: events.archive @ event(:id)",
		"service elsewhere":     "GET    /api/events/:id | gate: - | layers: eventself.requireSecret | keys: - @ service(render)",
		"service without layer": "GET    /api/render/key | gate: - | layers: - | keys: - @ service(render)",
		"bearer elsewhere":      "GET    /api/events/:id/results | gate: - | layers: eventself.liveScreenGate | keys: - @ bearer(live-screen)",
		"bearer without layer":  "GET    /api/events/self/live-screen | gate: - | layers: - | keys: - @ bearer(live-screen)",
		"own team param":        "GET    /api/events/self/team | gate: self | layers: - | keys: (team-member) @ own-team(:id)",
	}
	for name, line := range bad {
		if got := declarationViolations(line, nil, nil); len(got) == 0 {
			t.Errorf("%s: %q passed", name, line)
		}
	}

	// A reset to a default keeps update (Appendix A.3): the exception lets it through.
	reset := "DELETE /api/settings/retention | gate: platform.settings.write | layers: - | keys: platform.settings.update @ platform"
	if got := declarationViolations(reset, nil, nil); len(got) == 0 {
		t.Errorf("a DELETE route with an update key passed without the exception")
	}
	if got := declarationViolations(reset, map[string]string{"DELETE /api/settings/retention": "reset to the default"}, nil); len(got) != 0 {
		t.Errorf("an excepted reset still fails: %v", got)
	}
	// A stale exception (the route follows the rule, or is not declared) fails.
	if got := declarationViolations("DELETE /api/users/:userID | gate: users.delete | layers: - | keys: users.delete @ platform", map[string]string{"DELETE /api/users/:userID": "x"}, nil); len(got) == 0 {
		t.Errorf("a stale delete exception passed")
	}
	if got := declarationViolations("GET    /api/users | gate: users.read | layers: - | keys: users.list @ platform", map[string]string{"DELETE /api/nowhere": "x"}, nil); len(got) == 0 {
		t.Errorf("an exception for an undeclared route passed")
	}
}

func TestParseExceptions(t *testing.T) {
	got, problems := parseExceptions("routes.delete-exceptions", "# comment\n\nDELETE /api/auth/account | own account\nDELETE  /api/x | y\nbroken line\nDELETE /api/auth/account | again\n")
	if len(got) != 2 || got["DELETE /api/x"] != "y" {
		t.Errorf("parsed %v", got)
	}
	if len(problems) != 2 {
		t.Errorf("want the malformed and the duplicate line reported, got %v", problems)
	}
}

func TestParticipantRoutesDeclareARelation(t *testing.T) {
	ok := strings.Join([]string{
		"GET    /api/events/:id/teams/mine/members | gate: self | layers: - | keys: (team-member) @ event(:id)",
		"GET    /api/events/self/public-info | gate: events.content.read(public) | layers: - | keys: - @ public(pages)",
		"GET    /api/events/self/participant-form | gate: self | layers: - | keys: self @ tenant",
	}, "\n")
	listed := map[string]string{"GET /api/events/self/participant-form": "A.3 #441: filled before approval"}
	if got := declarationViolations(ok, nil, listed); len(got) != 0 {
		t.Fatalf("want no violations, got %v", got)
	}
	if got := declarationViolations("GET    /api/events/self/participant-form | gate: self | layers: - | keys: self @ tenant", nil, nil); len(got) != 1 {
		t.Errorf("a participant route without a relation passed: %v", got)
	}
	if got := declarationViolations("GET    /api/events/self/participant-form | gate: self | layers: - | keys: event.read @ tenant", nil, listed); len(got) == 0 {
		t.Errorf("a listed participant route with staff keys alone passed")
	}
	if got := declarationViolations("GET    /api/events/:id/lobby | gate: self | layers: eventself.touchPresence | keys: self @ event(:id)", nil, nil); len(got) == 0 {
		t.Errorf("a participant route outside the prefixes (presence layer) passed without a relation")
	}
	stale := map[string]string{"GET /api/events/:id/teams/mine/members": "x"}
	if got := declarationViolations("GET    /api/events/:id/teams/mine/members | gate: self | layers: - | keys: (team-member) @ event(:id)", nil, stale); len(got) == 0 {
		t.Errorf("a stale relation exception passed")
	}
	if got := declarationViolations("GET    /api/users | gate: users.read | layers: - | keys: users.list @ platform", nil, map[string]string{"GET /api/events/self/info": "x"}); len(got) == 0 {
		t.Errorf("a relation exception for an undeclared route passed")
	}
}
