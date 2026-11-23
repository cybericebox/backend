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
	r.GET("users", h.prot.Route(authz.PublicRoute(""), nil), h.list)
}`)
	if len(got) != 1 {
		t.Fatalf("want 1 violation, got %v", got)
	}
}

func TestDeclarationViolations(t *testing.T) {
	golden := strings.Join([]string{
		"GET    /api/users | gate: users.read | layers: - | keys: users.list @ platform",
		"GET    /api/auth/me | gate: self | layers: - | keys: self @ self",
		"POST   /api/auth/sign-in | gate: - | layers: recaptcha | keys: - @ public",
		"GET    /api/events/:id | gate: events.read | layers: - | keys: ?",
		"DELETE /api/users/:userID | gate: users.delete | layers: - | keys: users.delete @ platform",
		"DELETE /api/auth/sessions/:id | gate: self | layers: - | keys: self @ self",
		"DELETE /api/auth/last-email | gate: - | layers: - | keys: - @ public",
		"GET    /api/admin/assistant/statistics/events/:eventID | gate: platform.settings.read | layers: - | keys: cyberbrain.stats.read @ event(:eventID)",
	}, "\n")
	if got := declarationViolations(golden, nil); len(got) != 0 {
		t.Fatalf("want no violations, got %v", got)
	}

	bad := map[string]string{
		"unknown key":         "GET    /api/users | gate: users.read | layers: - | keys: users.teleport @ platform",
		"scope source":        "POST   /api/me/draft-key | gate: self | layers: - | keys: event.exercises.create @ platform",
		"delete without key":  "DELETE /api/users/:userID | gate: users.delete | layers: - | keys: users.status.update @ platform",
		"reject without key":  "POST   /api/events/:id/participants/:userID/reject | gate: events.write | layers: - | keys: event.participants.update @ event(:id)",
		"bare self in manage": "GET    /api/events/:id/manage/teams | gate: self | layers: event.requireRead | keys: self @ event(:id)",
		"bare self exercises": "GET    /api/exercises | gate: self | layers: - | keys: self @ self",
		"bare self assistant": "GET    /api/assistant/sessions | gate: self | layers: - | keys: self @ self",
		"unreadable":          "GET    /api/users | gate: users.read | layers: - | keys: users.list",
		"no keys column":      "GET    /api/users | gate: users.read | layers: -",
		"event param missing": "POST   /api/events/bulk/archive | gate: events.write | layers: - | keys: events.archive @ event(:id)",
	}
	for name, line := range bad {
		if got := declarationViolations(line, nil); len(got) == 0 {
			t.Errorf("%s: %q passed", name, line)
		}
	}

	// A reset to a default keeps update (Appendix A.3): the allowlist lets it through.
	reset := "DELETE /api/settings/retention | gate: platform.settings.write | layers: - | keys: platform.settings.update @ platform"
	if got := declarationViolations(reset, nil); len(got) == 0 {
		t.Errorf("a DELETE route with an update key passed without the allowlist")
	}
	if got := declarationViolations(reset, map[string]string{"DELETE /api/settings/retention": "reset to the default"}); len(got) != 0 {
		t.Errorf("an allowlisted reset still fails: %v", got)
	}
	// A stale allowlist entry (the route is not declared, or now has a delete key) fails.
	if got := declarationViolations("DELETE /api/users/:userID | gate: users.delete | layers: - | keys: users.delete @ platform", map[string]string{"DELETE /api/users/:userID": "x"}); len(got) == 0 {
		t.Errorf("a stale delete exception passed")
	}
}
