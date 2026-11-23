// Command routekeys extracts the expected route declarations from Appendix A.3 of the access spec
// (docs/internal-docs: platform/specs/access-control-design.md) into
// internal/delivery/controller/http/handler/testdata/routes.expected-keys, which TestDeclarationsFollowAppendixA3
// compares with the keys column of routes.golden. The spec is the source of truth per route (plan P1b): the
// declarations are read from its tables, never derived again by hand. Rows the reader cannot turn into a declaration
// are written as "?" with the reason, and stay undeclared until the spec or overrides.txt settles them.
//
// overrides.txt records owner and review decisions that the spec does not show yet, one per line:
// "METHOD /api/path | keys: <declaration> | data: <key,key> | reason" (keys: and data: optional, the reason not).
//
// Usage (from the repository root, after a change of the spec or of overrides.txt):
//
//	go run ./tools/routekeys -spec ../../docs/internal-docs/platform/specs/access-control-design.md
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/cybericebox/backend/internal/authz"
)

const defaultOut = "internal/delivery/controller/http/handler/testdata/routes.expected-keys"

func main() {
	spec := flag.String("spec", "../../docs/internal-docs/platform/specs/access-control-design.md", "the access spec")
	overrides := flag.String("overrides", "tools/routekeys/overrides.txt", "owner and review decisions")
	out := flag.String("out", defaultOut, "the expected declarations")
	commit := flag.String("spec-commit", "", "the spec commit to record (default: the last commit of the spec file)")
	flag.Parse()

	sha := *commit
	if sha == "" {
		var err error
		if sha, err = specCommit(*spec); err != nil {
			fail(err)
		}
	}

	specFile, err := os.Open(*spec)
	if err != nil {
		fail(err)
	}
	defer specFile.Close()
	routes, err := readAppendix(specFile)
	if err != nil {
		fail(err)
	}
	overrideText, err := os.ReadFile(*overrides)
	if err != nil {
		fail(err)
	}
	if err := applyOverrides(routes, string(overrideText)); err != nil {
		fail(err)
	}
	if err := os.WriteFile(*out, []byte(render(routes, sha)), 0o644); err != nil {
		fail(err)
	}
}

// specCommit is the last commit of the spec file; a spec with uncommitted changes is refused, so the recorded commit
// always names what the file was generated from.
func specCommit(spec string) (string, error) {
	dir, name := filepath.Split(spec)
	if dir == "" {
		dir = "."
	}
	status, err := exec.Command("git", "-C", dir, "status", "--porcelain", "--", name).Output()
	if err != nil {
		return "", fmt.Errorf("git status of the spec: %w", err)
	}
	if strings.TrimSpace(string(status)) != "" {
		return "", fmt.Errorf("%s has uncommitted changes: commit them first", spec)
	}
	sha, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%H", "--", name).Output()
	if err != nil || strings.TrimSpace(string(sha)) == "" {
		return "", fmt.Errorf("the last commit of the spec: %v", err)
	}
	return strings.TrimSpace(string(sha)), nil
}

// SpecCommitLine is the header line of routes.expected-keys and overrides.txt that names the spec commit.
const SpecCommitLine = "# spec: "

func fail(err error) {
	fmt.Fprintln(os.Stderr, "routekeys:", err)
	os.Exit(1)
}

// expected is one route of Appendix A.3 and what it declares.
type expected struct {
	method, path, row string
	decl              string // the declaration, or "?"
	data              []authz.Key
	note              string
}

var (
	rowPattern   = regexp.MustCompile("^\\| (\\d+) \\| `([A-Z]+) ([^`]+)` \\|")
	keyPattern   = regexp.MustCompile("`([a-z][a-z0-9_.\\-]*)`")
	eventPattern = regexp.MustCompile("^path :(\\w+) → event")
)

// readAppendix reads the route tables of Appendix A.3 (A.3.2 to A.3.13; A.3.1 is outside the API registry and A.3.14
// lists routes not built yet).
func readAppendix(r io.Reader) (map[string]*expected, error) {
	out := map[string]*expected{}
	inRoutes := false
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "#### A.3.1 "), strings.HasPrefix(line, "#### A.3.14 "), strings.HasPrefix(line, "### A.4"):
			inRoutes = false
			continue
		case strings.HasPrefix(line, "#### A.3."):
			inRoutes = true
			continue
		}
		if !inRoutes {
			continue
		}
		m := rowPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		cells := strings.Split(line, " | ")
		if len(cells) < 7 {
			return nil, fmt.Errorf("A.3 #%s: want 8 columns, got %d", m[1], len(cells))
		}
		path := m[3]
		if !strings.HasPrefix(path, "/api/") {
			path = "/api" + path
		}
		e := &expected{method: m[2], path: path, row: m[1]}
		decl, reason := convert(strings.TrimSpace(cells[4]), strings.TrimSpace(cells[5]))
		if reason != "" {
			e.decl, e.note = "?", reason
		} else {
			e.decl = decl.String()
		}
		e.data = dataKeys(cells[6])
		id := e.method + " " + e.path
		if _, dup := out[id]; dup {
			return nil, fmt.Errorf("A.3 #%s: %s listed twice", m[1], id)
		}
		out[id] = e
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no route rows found in Appendix A.3")
	}
	return out, nil
}

// convert turns the keys and scope cells of a row into a declaration, or explains why it cannot.
func convert(keys, scope string) (authz.Decl, string) {
	switch {
	case strings.HasPrefix(keys, "service secret (X-Render-Secret)"):
		return authz.ServiceRoute(authz.ServiceRender), ""
	case strings.HasPrefix(keys, "bearer token (live-screen link)"):
		return authz.BearerRoute(authz.BearerLiveScreen), ""
	case strings.HasPrefix(keys, "`public`"):
		var objects []string
		if strings.Contains(keys, "visibility: event pages") {
			objects = append(objects, authz.PublicPages)
		}
		if strings.Contains(keys, "visibility: scoreboard") {
			objects = append(objects, authz.PublicScoreboard)
		}
		if strings.Contains(keys, "results after it") {
			objects = append(objects, authz.PublicResults)
		}
		return authz.PublicRoute(objects...), ""
	}
	// "`k`; an archived event: …" — the part after the semicolon is a state rule (data column / overrides).
	if before, _, ok := strings.Cut(keys, "; "); ok && strings.HasPrefix(before, "`") && !strings.Contains(before, ",") {
		keys = before
	}

	var d authz.Decl
	switch {
	case strings.HasPrefix(keys, "all-of: ") && strings.Contains(keys, " and "):
		head, tail, _ := strings.Cut(strings.TrimPrefix(keys, "all-of: "), " and ")
		all, rel, ok := items(head)
		if !ok || rel != "" {
			return d, "keys cell not in a declarable form"
		}
		tail = strings.TrimPrefix(tail, "any-of ")
		if !strings.HasPrefix(tail, "(") || !strings.HasSuffix(tail, ")") {
			return d, "keys cell not in a declarable form"
		}
		anyOf, rel, ok := items(strings.ReplaceAll(tail[1:len(tail)-1], " or ", ", "))
		if !ok {
			return d, "keys cell not in a declarable form"
		}
		d = authz.Decl{AllOf: all, AnyOf: anyOf, Relation: rel}
	case strings.HasPrefix(keys, "all-of: "):
		all, rel, ok := items(strings.TrimPrefix(keys, "all-of: "))
		if !ok || rel != "" {
			return d, "keys cell not in a declarable form"
		}
		d = authz.Decl{AllOf: all}
	case strings.HasPrefix(keys, "any-of: "):
		anyOf, rel, ok := items(strings.TrimPrefix(keys, "any-of: "))
		if !ok {
			return d, "keys cell not in a declarable form"
		}
		d = authz.Decl{AnyOf: anyOf, Relation: rel}
	default:
		anyOf, rel, ok := items(keys)
		if !ok || len(anyOf)+boolInt(rel != "") != 1 {
			return d, "keys cell not in a declarable form"
		}
		d = authz.Decl{AnyOf: anyOf, Relation: rel}
	}

	source, why := scopeOf(scope, d)
	if why != "" {
		return authz.Decl{}, why
	}
	d.Scope = source
	if problems := d.Problems(); len(problems) > 0 {
		return authz.Decl{}, "the row breaks the catalogue rules: " + strings.Join(problems, "; ")
	}
	return d, ""
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

var relations = map[string]authz.Relation{
	"participant": authz.RelParticipant, "team member": authz.RelTeamMember, "captain": authz.RelCaptain,
	"author": authz.RelAuthor,
}

// items reads "`a`, `b`, (author)" into keys and at most one relation.
func items(s string) ([]authz.Key, authz.Relation, bool) {
	var keys []authz.Key
	var rel authz.Relation
	for _, item := range strings.Split(s, ", ") {
		item = strings.TrimSpace(strings.Trim(strings.TrimSpace(item), "`"))
		switch {
		case strings.HasPrefix(item, "(") && strings.HasSuffix(item, ")"):
			r, ok := relations[item[1:len(item)-1]]
			if !ok || rel != "" {
				return nil, "", false
			}
			rel = r
		case item == "author":
			if rel != "" {
				return nil, "", false
			}
			rel = authz.RelAuthor
		case keyPattern.MatchString("`"+item+"`") && !strings.ContainsAny(item, " ()"):
			keys = append(keys, authz.Key(item))
		default:
			return nil, "", false
		}
	}
	return keys, rel, true
}

// scopeOf reads the scope cell. A source the reader does not know stays "?" for the owner to settle.
func scopeOf(scope string, d authz.Decl) (authz.ScopeSource, string) {
	switch {
	case scope == "—":
		if len(d.AllOf) == 0 && d.Relation == "" && len(d.AnyOf) == 1 {
			if spec, ok := authz.Lookup(d.AnyOf[0]); ok && spec.Allows(authz.ScopeImplicit) {
				return authz.SourceSelf, ""
			}
		}
		return "", "scope \"—\" with keys that are not implicit"
	case scope == "platform", strings.HasPrefix(scope, "platform ("), strings.HasPrefix(scope, "none"):
		return authz.SourcePlatform, ""
	case strings.HasPrefix(scope, "tenant (Origin)"):
		return authz.SourceTenant, ""
	case strings.HasPrefix(scope, "path :id → exercise"), strings.HasPrefix(scope, "path :id / body IDs → exercises"),
		strings.HasPrefix(scope, "body IDs → each exercise"), strings.HasPrefix(scope, "body IDs → exercises"):
		return authz.SourceObject("exercise"), ""
	case strings.HasPrefix(scope, "own team (relation)"), strings.HasPrefix(scope, "own team (lookup)"):
		return authz.SourceOwnTeam("id"), ""
	case strings.HasPrefix(scope, "path :teamID → the caller's team"):
		return authz.SourceObject("team"), ""
	}
	if m := eventPattern.FindStringSubmatch(scope); m != nil {
		return authz.SourceEventParam(m[1]), ""
	}
	return "", fmt.Sprintf("scope %q is not a scope source of the plan", scope)
}

// dataKeys are the catalogue keys the data-level column names.
func dataKeys(cell string) []authz.Key {
	var out []authz.Key
	for _, m := range keyPattern.FindAllStringSubmatch(cell, -1) {
		k := authz.Key(m[1])
		if _, ok := authz.Lookup(k); ok && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

func applyOverrides(routes map[string]*expected, text string) error {
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, " | ")
		id := strings.Join(strings.Fields(parts[0]), " ")
		e, ok := routes[id]
		if !ok {
			return fmt.Errorf("overrides.txt:%d: %s is not a route of Appendix A.3", n+1, id)
		}
		if len(parts) < 3 {
			return fmt.Errorf("overrides.txt:%d: want \"METHOD /api/path | keys: … | data: … | reason\"", n+1)
		}
		reason := strings.TrimSpace(parts[len(parts)-1])
		for _, p := range parts[1 : len(parts)-1] {
			switch {
			case strings.HasPrefix(p, "keys: "):
				d, err := authz.ParseDecl(strings.TrimPrefix(p, "keys: "))
				if err != nil {
					return fmt.Errorf("overrides.txt:%d: %w", n+1, err)
				}
				if problems := d.Problems(); len(problems) > 0 {
					return fmt.Errorf("overrides.txt:%d: %s", n+1, strings.Join(problems, "; "))
				}
				e.decl = d.String()
			case strings.HasPrefix(p, "data: "):
				e.data = nil
				for _, k := range strings.Split(strings.TrimPrefix(p, "data: "), ",") {
					key := authz.Key(strings.TrimSpace(k))
					if _, ok := authz.Lookup(key); !ok {
						return fmt.Errorf("overrides.txt:%d: data key %s is not in the catalogue", n+1, key)
					}
					e.data = append(e.data, key)
				}
			default:
				return fmt.Errorf("overrides.txt:%d: unknown field %q", n+1, p)
			}
		}
		e.note = "override: " + reason
	}
	return nil
}

func render(routes map[string]*expected, sha string) string {
	list := make([]*expected, 0, len(routes))
	for _, e := range routes {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].path != list[j].path {
			return list[i].path < list[j].path
		}
		return list[i].method < list[j].method
	})
	var b strings.Builder
	b.WriteString("# Generated by tools/routekeys from Appendix A.3 of platform/specs/access-control-design.md and\n")
	b.WriteString("# tools/routekeys/overrides.txt; do not edit. METHOD path | keys: declaration or ? | data: keys | A.3 row.\n")
	b.WriteString(SpecCommitLine + sha + "\n")
	for _, e := range list {
		data := "-"
		if len(e.data) > 0 {
			parts := make([]string, len(e.data))
			for i, k := range e.data {
				parts[i] = string(k)
			}
			data = strings.Join(parts, ",")
		}
		source := "A.3 #" + e.row
		if e.note != "" {
			source += "; " + e.note
		}
		fmt.Fprintf(&b, "%-6s %s | keys: %s | data: %s | %s\n", e.method, e.path, e.decl, data, source)
	}
	return b.String()
}
