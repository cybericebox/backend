package main

import (
	"fmt"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/cybericebox/backend/internal/authz"
)

// The declaration rules of plan P1b, checked over the keys column of routes.golden (the mounted tree, which
// TestRoutePolicy keeps equal to the router): every declared key is in the catalogue and can be checked at the
// route's scope source (authz.Decl.Problems); an event(:param) source names a parameter of the path; a delete-shaped
// route (DELETE, or a reject, revoke, remove, detach or delete action) declares its area's .delete key, and a .delete
// key stands only on a delete-shaped route; a service or bearer route sits under its prefix behind its credential layer; the event
// manage area, the catalog and the assistant never stand on a bare self. A route still marked "?" is not declared yet
// (TestEveryRouteDeclaresKeys holds that list).
//
// The delete-shaped routes that keep an update key, own data, a relation or a public flow (Appendix A.3: resets to a
// default, undo routes, the caller's own data), and the routes that hold a .delete key without being delete-shaped,
// are listed one by one with their reason in testdata/routes.delete-exceptions ("METHOD /api/path | reason").

// bareSelfForbidden are the areas where self alone is never the declaration (staff work, the catalog, the
// assistant): the real check is a key, a relation or an object scope.
var bareSelfForbidden = []string{"/api/events/:id/manage", "/api/exercises", "/api/assistant"}

// participantPrefixes are the participant routes (spec §3, plan P1b area 5): they admit the caller through the
// participation relation (participant, team member, captain), not through keys. A participant route that A.3 gives
// self (the caller's own data before or beside participation: joining, the participant form, own answers) is listed
// with its row in testdata/routes.relation-exceptions.
var participantPrefixes = []string{
	"/api/events/self", "/api/events/:id/teams", "/api/events/:id/forms",
	"/api/events/:id/results/team", "/api/events/:id/results/participation",
}

// deleteActions are the last path segments that remove something although the method is not DELETE.
var deleteActions = []string{"reject", "revoke", "remove", "detach", "delete"}

// credentialRoutes: where a service or bearer source may stand, and the layer that checks its credential.
var credentialRoutes = map[string]struct{ prefix, layer string }{
	"service(" + authz.ServiceRender + ")":   {prefix: "/api/render/", layer: "requireSecret"},
	"bearer(" + authz.BearerLiveScreen + ")": {prefix: "/api/events/self/live-screen", layer: "liveScreenGate"},
}

func testdataPath(handlerRoot, name string) string {
	return path.Join(handlerRoot, "testdata", name)
}

func readTestdata(handlerRoot, name string) (string, error) {
	b, err := os.ReadFile(testdataPath(handlerRoot, name))
	return string(b), err
}

// parseExceptions reads a list of routes (routes.delete-exceptions, routes.relation-exceptions): one
// "METHOD /api/path | reason" per line; # starts a comment.
func parseExceptions(name, text string) (map[string]string, []string) {
	out := map[string]string{}
	var problems []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		id, reason, ok := strings.Cut(line, " | ")
		id = strings.Join(strings.Fields(id), " ")
		if !ok || strings.TrimSpace(reason) == "" || len(strings.Fields(id)) != 2 {
			problems = append(problems, fmt.Sprintf("%s: want \"METHOD /api/path | reason\", got %q", name, line))
			continue
		}
		if _, dup := out[id]; dup {
			problems = append(problems, fmt.Sprintf("%s lists %s twice", name, id))
		}
		out[id] = strings.TrimSpace(reason)
	}
	return out, problems
}

// declarationViolations checks every line of routes.golden; exceptions is the delete allowlist, relationExceptions the
// participant routes without a relation.
func declarationViolations(golden string, exceptions, relationExceptions map[string]string) []string {
	var out []string
	used := map[string]bool{}
	usedRelation := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(golden), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			out = append(out, fmt.Sprintf("routes.golden: unreadable line %q", line))
			continue
		}
		method, route := fields[0], fields[1]
		id := method + " " + route
		rest, keys, ok := strings.Cut(line, " | keys: ")
		if !ok {
			out = append(out, fmt.Sprintf("%s: routes.golden has no keys column", id))
			continue
		}
		var layers []string
		if _, l, ok := strings.Cut(rest, " | layers: "); ok && l != "-" {
			layers = strings.Split(l, ",")
		}
		keys = strings.TrimSpace(keys)
		if keys == "?" {
			continue
		}
		decl, err := authz.ParseDecl(keys)
		if err != nil {
			out = append(out, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		for _, p := range decl.Problems() {
			out = append(out, id+": "+p)
		}
		if param := decl.Scope.Param(); param != "" && !slices.Contains(strings.Split(route, "/"), ":"+param) {
			out = append(out, fmt.Sprintf("%s: scope %s names a path parameter the route does not have", id, decl.Scope))
		}
		out = append(out, serviceViolations(id, route, decl, layers)...)
		if isBareSelf(decl) {
			for _, prefix := range bareSelfForbidden {
				if route == prefix || strings.HasPrefix(route, prefix+"/") {
					out = append(out, fmt.Sprintf("%s: bare self under %s: declare the key, relation or object scope of Appendix A.3", id, prefix))
				}
			}
		}
		if _, staff := staffRoutes[id]; !staff && isParticipantRoute(route, layers) {
			_, listed := relationExceptions[id]
			if listed {
				usedRelation[id] = true
			}
			kind := decl.Scope.Kind()
			missing := decl.Relation == "" && kind != "public" && kind != "service" && kind != "bearer"
			switch {
			case missing && !listed:
				out = append(out, fmt.Sprintf("%s: a participant route declares no relation (%s); a route A.3 gives self goes on routes.relation-exceptions with its row", id, decl))
			case listed && !isBareSelf(decl):
				out = append(out, fmt.Sprintf("%s: on routes.relation-exceptions, which admits only a bare self, but declares %s", id, decl))
			}
		}
		_, excepted := exceptions[id]
		if excepted {
			used[id] = true
		}
		var broken string
		if isDeleteRoute(method, route) {
			if !hasDeleteKey(decl) {
				broken = fmt.Sprintf("%s: a delete route declares no .delete key (%s)", id, decl)
			}
		} else if k, ok := deleteKeyOf(decl); ok {
			broken = fmt.Sprintf("%s: %s on a route that is not delete-shaped", id, k)
		}
		switch {
		case broken != "" && !excepted:
			out = append(out, broken+"; a reset, undo, own-data, relation or public route goes on routes.delete-exceptions with its A.3 row")
		case broken == "" && excepted:
			out = append(out, fmt.Sprintf("%s: on routes.delete-exceptions but follows the delete rule: drop the entry", id))
		}
	}
	for id := range exceptions {
		if !used[id] {
			out = append(out, fmt.Sprintf("routes.delete-exceptions lists %s, which is not a declared route", id))
		}
	}
	for id := range relationExceptions {
		if !usedRelation[id] {
			out = append(out, fmt.Sprintf("routes.relation-exceptions lists %s, which is not a declared participant route", id))
		}
	}
	slices.Sort(out)
	return out
}

func serviceViolations(id, route string, d authz.Decl, layers []string) []string {
	if kind := d.Scope.Kind(); kind != "service" && kind != "bearer" {
		return nil
	}
	where, ok := credentialRoutes[string(d.Scope)]
	if !ok {
		return []string{fmt.Sprintf("%s: no route rule for %s", id, d.Scope)}
	}
	var out []string
	if route != where.prefix && !strings.HasPrefix(route, strings.TrimSuffix(where.prefix, "/")+"/") {
		out = append(out, fmt.Sprintf("%s: %s only under %s", id, d.Scope, where.prefix))
	}
	if !slices.ContainsFunc(layers, func(l string) bool { return strings.HasSuffix(l, "."+where.layer) || l == where.layer }) {
		out = append(out, fmt.Sprintf("%s: %s without its credential layer %s", id, d.Scope, where.layer))
	}
	return out
}

// staffRoutes sit on participant paths but are staff routes: they declare keys, not a relation.
var staffRoutes = map[string]string{
	"POST /api/events/self/render-grant": "A.3 #446: the render grant for staff previews (event.read); participants wait for an owner decision",
}

// participantLayers mark a participant route wherever it is mounted: the presence bookkeeping of the event site and
// the tenant resolution of its Origin. A new participant route outside participantPrefixes is still checked.
var participantLayers = []string{"touchPresence", "ResolveEventTenant"}

func isParticipantRoute(route string, layers []string) bool {
	if slices.ContainsFunc(participantPrefixes, func(p string) bool { return route == p || strings.HasPrefix(route, p+"/") }) {
		return true
	}
	return slices.ContainsFunc(layers, func(l string) bool {
		return slices.ContainsFunc(participantLayers, func(p string) bool { return strings.Contains(l, p) })
	})
}

func isBareSelf(d authz.Decl) bool {
	return len(d.AllOf) == 0 && d.Relation == "" && len(d.AnyOf) == 1 && d.AnyOf[0] == authz.Self
}

func isDeleteRoute(method, route string) bool {
	if method == "DELETE" {
		return true
	}
	return slices.Contains(deleteActions, path.Base(route))
}

func isDeleteKey(k authz.Key) bool { return strings.HasSuffix(string(k), ".delete") }

func grantable(keys []authz.Key) []authz.Key {
	var out []authz.Key
	for _, k := range keys {
		if spec, ok := authz.Lookup(k); ok && spec.Grantable() {
			out = append(out, k)
		}
	}
	return out
}

// hasDeleteKey: the grantable keys of the declaration remove something. Every any-of key is a .delete key, or,
// without grantable any-of keys, one all-of key is. A declaration without grantable keys (own data, a relation, a
// public flow) has none: such a delete route is listed in routes.delete-exceptions.
func hasDeleteKey(d authz.Decl) bool {
	anyOf, allOf := grantable(d.AnyOf), grantable(d.AllOf)
	if len(anyOf) > 0 {
		for _, k := range anyOf {
			if !isDeleteKey(k) {
				return false
			}
		}
		return true
	}
	return slices.ContainsFunc(allOf, isDeleteKey)
}

// deleteKeyOf returns a .delete key the declaration names, if any.
func deleteKeyOf(d authz.Decl) (authz.Key, bool) {
	for _, k := range d.Keys() {
		if isDeleteKey(k) {
			return k, true
		}
	}
	return "", false
}
