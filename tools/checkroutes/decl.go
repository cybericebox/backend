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
// route's scope source (authz.Decl.Problems); a DELETE route and every reject, revoke, remove or detach action
// declares its area's .delete key; an event(:param) source names a parameter of the path; the event manage area, the
// catalog and the assistant never stand on a bare self.
// A route still marked "?" is not declared yet (TestEveryRouteDeclaresKeys holds that list).

// deleteExceptions are the delete-shaped routes that keep an update key or a relation by Appendix A.3: resets to a
// default, undo routes (discarding a draft) and the caller's own data. Keyed "METHOD /api/path".
var deleteExceptions = map[string]string{}

// bareSelfForbidden are the areas where self alone is never the declaration (staff work, the catalog, the
// assistant): the real check is a key, a relation or an object scope.
var bareSelfForbidden = []string{"/api/events/:id/manage", "/api/exercises", "/api/assistant"}

// deleteActions are the last path segments that remove something although the method is not DELETE.
var deleteActions = []string{"reject", "revoke", "remove", "detach"}

func goldenPath(handlerRoot string) string {
	return path.Join(handlerRoot, "testdata", "routes.golden")
}

func readGolden(handlerRoot string) (string, error) {
	b, err := os.ReadFile(goldenPath(handlerRoot))
	return string(b), err
}

// declarationViolations checks every line of routes.golden; exceptions is the delete allowlist.
func declarationViolations(golden string, exceptions map[string]string) []string {
	var out []string
	used := map[string]bool{}
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
		_, keys, ok := strings.Cut(line, " | keys: ")
		if !ok {
			out = append(out, fmt.Sprintf("%s: routes.golden has no keys column", id))
			continue
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
		if isBareSelf(decl) {
			for _, prefix := range bareSelfForbidden {
				if route == prefix || strings.HasPrefix(route, prefix+"/") {
					out = append(out, fmt.Sprintf("%s: bare self under %s: declare the key, relation or object scope of Appendix A.3", id, prefix))
				}
			}
		}
		if isDeleteRoute(method, route) {
			_, excepted := exceptions[id]
			if excepted {
				used[id] = true
			}
			missing := !hasDeleteKey(decl)
			switch {
			case missing && !excepted:
				out = append(out, fmt.Sprintf("%s: a delete route declares no .delete key (%s); a reset, undo or own-data route goes on deleteExceptions with its A.3 row", id, decl))
			case !missing && excepted:
				out = append(out, fmt.Sprintf("%s: on deleteExceptions but declares a .delete key: drop the entry", id))
			}
		}
	}
	for id := range exceptions {
		if !used[id] {
			out = append(out, fmt.Sprintf("deleteExceptions lists %s, which is not a declared delete route", id))
		}
	}
	slices.Sort(out)
	return out
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

// hasDeleteKey: the grantable keys of the declaration remove something. Every any-of key is a .delete key, or,
// without grantable any-of keys, one all-of key is. A declaration without grantable keys (own data, a relation,
// a public flow) needs none.
func hasDeleteKey(d authz.Decl) bool {
	grantable := func(keys []authz.Key) []authz.Key {
		var out []authz.Key
		for _, k := range keys {
			if spec, ok := authz.Lookup(k); ok && spec.Grantable() {
				out = append(out, k)
			}
		}
		return out
	}
	isDelete := func(k authz.Key) bool { return strings.HasSuffix(string(k), ".delete") }
	anyOf, allOf := grantable(d.AnyOf), grantable(d.AllOf)
	if len(anyOf) > 0 {
		for _, k := range anyOf {
			if !isDelete(k) {
				return false
			}
		}
		return true
	}
	if len(allOf) == 0 {
		return true
	}
	return slices.ContainsFunc(allOf, isDelete)
}
