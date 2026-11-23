package authz

import (
	"fmt"
	"slices"
	"strings"
)

// Decl is what a route declares (spec owner decision 1, plan P1b): the keys that admit the caller and where the
// scope of the check comes from. A caller passes when they hold every AllOf key and, when AnyOf or Relation is set,
// at least one AnyOf key or the Relation. In P1 the declaration is only recorded; the legacy gate still decides.
type Decl struct {
	AllOf []Key
	AnyOf []Key
	// Relation admits the caller instead of an AnyOf key: participation (spec §3) or authorship (§5.4), which are
	// relations, not keys.
	Relation Relation
	Scope    ScopeSource
	// Data are the keys that decide fields or rows of the answer (Appendix A.3, "data-level keys"): the route passes
	// without them, the answer is masked or filtered. They are recorded so the enforcement (P2, P4) keeps them.
	Data []Key
}

// Relation is a relation of the caller to the object of a route that admits them without a key.
type Relation string

const (
	RelParticipant Relation = "participant"
	RelTeamMember  Relation = "team-member"
	RelCaptain     Relation = "captain"
	RelAuthor      Relation = "author"
)

var relations = []Relation{RelParticipant, RelTeamMember, RelCaptain, RelAuthor}

// ScopeSource is where a route takes the scope of its check from. It is written as it reads in routes.golden:
// "platform", "event(:id)", "own-team(:id)", "tenant", "object(exercise)", "self", "public", "public(pages)",
// "service(render)" or "bearer(live-screen)".
type ScopeSource string

const (
	// SourcePlatform: platform scope; an event key is checked there only when it is platform-grantable (§9 Q20).
	SourcePlatform ScopeSource = "platform"
	// SourceTenant: the event the request's Origin resolves to (participant routes on the event site).
	SourceTenant ScopeSource = "tenant"
	// SourceSelf: the caller's own data; only implicit keys.
	SourceSelf ScopeSource = "self"
)

// SourceEventParam: the event named by the path parameter (":id", ":eventID").
func SourceEventParam(param string) ScopeSource { return ScopeSource("event(:" + param + ")") }

// SourceOwnTeam: the caller's own team in the event named by the path parameter (the team routes that name no team,
// /events/:id/teams/mine*): the event scope for keys, the team for the relation.
func SourceOwnTeam(param string) ScopeSource { return ScopeSource("own-team(:" + param + ")") }

// SourceObject: the scope of an object the route addresses (a task, a team, a deploy): its owner event, else the
// platform.
func SourceObject(kind string) ScopeSource { return ScopeSource("object(" + kind + ")") }

// The public objects of an event, each with its own visibility setting (spec §4.5).
const (
	PublicPages      = "pages"
	PublicScoreboard = "scoreboard"
	PublicResults    = "results"
)

var publicObjects = []string{PublicPages, PublicScoreboard, PublicResults}

// SourcePublic: a route open to everyone; objects name the public visibility settings it reads (spec §4.5), none for
// public flows that read none (sign-in, password reset). The standings read the scoreboard while the event runs and
// the results after it: SourcePublic(PublicScoreboard, PublicResults).
func SourcePublic(objects ...string) ScopeSource {
	objects = slices.DeleteFunc(slices.Clone(objects), func(o string) bool { return o == "" })
	if len(objects) == 0 {
		return "public"
	}
	return ScopeSource("public(" + strings.Join(objects, ",") + ")")
}

// The services that call the API with a credential of their own instead of a user session (a fixed list).
const (
	// ServiceRender: the event-frontend server, with the render secret (X-Render-Secret).
	ServiceRender = "render"
)

var services = []string{ServiceRender}

// SourceService: a route for a service of the platform, authorized by the service's own credential, never by a user's
// keys; the route carries the credential check as a layer.
func SourceService(name string) ScopeSource { return ScopeSource("service(" + name + ")") }

// The bearer links: a secret link that stands for the holder, with no session (a fixed list).
const (
	// BearerLiveScreen: a live-screen link of an event (spec §7.5), checked by the live-screen gate.
	BearerLiveScreen = "live-screen"
)

var bearers = []string{BearerLiveScreen}

// SourceBearer: a route authorized by a bearer link, never by a user's keys; the route carries the link check as a
// layer.
func SourceBearer(name string) ScopeSource { return ScopeSource("bearer(" + name + ")") }

// PublicObjects returns the visibility objects a public source names.
func (s ScopeSource) PublicObjects() []string {
	if s.Kind() != "public" || s == "public" {
		return nil
	}
	_, rest, _ := strings.Cut(string(s), "(")
	return strings.Split(strings.TrimSuffix(rest, ")"), ",")
}

// Param returns the path parameter of an event(:param) or own-team(:param) source.
func (s ScopeSource) Param() string {
	if kind := s.Kind(); kind != "event" && kind != "own-team" {
		return ""
	}
	_, rest, _ := strings.Cut(string(s), "(:")
	return strings.TrimSuffix(rest, ")")
}

// Kind is the source without its argument: platform, event, tenant, object, self or public.
func (s ScopeSource) Kind() string {
	kind, _, _ := strings.Cut(string(s), "(")
	return kind
}

func (s ScopeSource) valid() bool {
	str := string(s)
	switch str {
	case string(SourcePlatform), string(SourceTenant), string(SourceSelf), "public":
		return true
	}
	kind, rest, ok := strings.Cut(str, "(")
	if !ok || !strings.HasSuffix(rest, ")") {
		return false
	}
	arg := strings.TrimSuffix(rest, ")")
	switch kind {
	case "event", "own-team":
		return len(arg) > 1 && arg[0] == ':' && isName(arg[1:])
	case "bearer":
		return slices.Contains(bearers, arg)
	case "object":
		return isName(arg)
	case "service":
		return slices.Contains(services, arg)
	case "public":
		objects := strings.Split(arg, ",")
		for i, o := range objects {
			if !slices.Contains(publicObjects, o) || slices.Contains(objects[:i], o) {
				return false
			}
		}
		return true
	}
	return false
}

func isName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// AnyOf declares a route that admits a holder of any of keys at scope.
func AnyOf(scope ScopeSource, keys ...Key) Decl { return Decl{AnyOf: keys, Scope: scope} }

// AllOf declares a route that needs every one of keys at scope.
func AllOf(scope ScopeSource, keys ...Key) Decl { return Decl{AllOf: keys, Scope: scope} }

// OwnData declares a route on the caller's own data: the implicit key self.
func OwnData() Decl { return AnyOf(SourceSelf, Self) }

// PublicRoute declares a route open to everyone; objects as in SourcePublic.
func PublicRoute(objects ...string) Decl { return Decl{Scope: SourcePublic(objects...)} }

// ServiceRoute declares a route of a platform service (SourceService): no keys, no relation.
func ServiceRoute(name string) Decl { return Decl{Scope: SourceService(name)} }

// BearerRoute declares a route authorized by a bearer link (SourceBearer): no keys, no relation.
func BearerRoute(name string) Decl { return Decl{Scope: SourceBearer(name)} }

// Related declares a route that admits the caller through a relation (participant, team member, captain, author).
func Related(scope ScopeSource, rel Relation) Decl { return Decl{Relation: rel, Scope: scope} }

// Or admits the caller through rel as well as through the AnyOf keys.
func (d Decl) Or(rel Relation) Decl {
	d.Relation = rel
	return d
}

// And adds keys the caller must hold on top of the rest.
func (d Decl) And(keys ...Key) Decl {
	d.AllOf = append(slices.Clone(d.AllOf), keys...)
	return d
}

// WithData records the data-level keys of the route (Decl.Data).
func (d Decl) WithData(keys ...Key) Decl {
	d.Data = append(slices.Clone(d.Data), keys...)
	return d
}

// Keys returns every key the declaration names, sorted.
func (d Decl) Keys() []Key {
	out := append(slices.Clone(d.AllOf), d.AnyOf...)
	slices.Sort(out)
	return slices.Compact(out)
}

// Empty reports a zero declaration (a route that declares nothing).
func (d Decl) Empty() bool {
	return d.Scope == "" && len(d.AllOf) == 0 && len(d.AnyOf) == 0 && d.Relation == "" && len(d.Data) == 0
}

// String renders the declaration as routes.golden shows it: "users.list @ platform",
// "allOf(a,b) + anyOf(c,(author)) @ object(exercise)", "- @ public", with " data(k,k)" after the scope when the route
// has data-level keys.
func (d Decl) String() string {
	anyOf := make([]string, 0, len(d.AnyOf)+1)
	for _, k := range d.AnyOf {
		anyOf = append(anyOf, string(k))
	}
	if d.Relation != "" {
		anyOf = append(anyOf, "("+string(d.Relation)+")")
	}
	allOf := make([]string, 0, len(d.AllOf))
	for _, k := range d.AllOf {
		allOf = append(allOf, string(k))
	}
	var parts []string
	switch {
	case len(allOf) == 0 && len(anyOf) == 1:
		parts = append(parts, anyOf[0])
	case len(allOf) == 0 && len(anyOf) == 0:
		parts = append(parts, "-")
	default:
		if len(allOf) > 0 {
			parts = append(parts, "allOf("+strings.Join(allOf, ",")+")")
		}
		if len(anyOf) > 0 {
			parts = append(parts, "anyOf("+strings.Join(anyOf, ",")+")")
		}
	}
	out := strings.Join(parts, " + ") + " @ " + string(d.Scope)
	if len(d.Data) > 0 {
		data := make([]string, len(d.Data))
		for i, k := range d.Data {
			data[i] = string(k)
		}
		out += " data(" + strings.Join(data, ",") + ")"
	}
	return out
}

// ParseDecl reads a declaration back from its String form (tools/checkroutes reads routes.golden with it).
func ParseDecl(s string) (Decl, error) {
	expr, scope, ok := strings.Cut(s, " @ ")
	if !ok {
		return Decl{}, fmt.Errorf("declaration %q: want \"<keys> @ <scope>\"", s)
	}
	var data []Key
	if before, list, ok := strings.Cut(scope, " data("); ok {
		if !strings.HasSuffix(list, ")") || len(list) == 1 {
			return Decl{}, fmt.Errorf("declaration %q: bad data list", s)
		}
		for _, k := range strings.Split(strings.TrimSuffix(list, ")"), ",") {
			if k == "" || strings.ContainsAny(k, " ()") {
				return Decl{}, fmt.Errorf("declaration %q: bad data key %q", s, k)
			}
			data = append(data, Key(k))
		}
		scope = before
	}
	d := Decl{Scope: ScopeSource(strings.TrimSpace(scope)), Data: data}
	if !d.Scope.valid() {
		return Decl{}, fmt.Errorf("declaration %q: unknown scope source %q", s, d.Scope)
	}
	expr = strings.TrimSpace(expr)
	if expr == "-" {
		return d, nil
	}
	for _, part := range strings.Split(expr, " + ") {
		var items []string
		var all bool
		switch {
		case strings.HasPrefix(part, "allOf(") && strings.HasSuffix(part, ")"):
			all = true
			items = strings.Split(strings.TrimSuffix(strings.TrimPrefix(part, "allOf("), ")"), ",")
		case strings.HasPrefix(part, "anyOf(") && strings.HasSuffix(part, ")"):
			items = strings.Split(strings.TrimSuffix(strings.TrimPrefix(part, "anyOf("), ")"), ",")
		case !strings.ContainsAny(part, ", "):
			items = []string{part}
		default:
			return Decl{}, fmt.Errorf("declaration %q: cannot read %q", s, part)
		}
		for _, item := range items {
			switch {
			case item == "" || strings.ContainsAny(item, " ()") && !(strings.HasPrefix(item, "(") && strings.HasSuffix(item, ")")):
				return Decl{}, fmt.Errorf("declaration %q: bad item %q", s, item)
			case strings.HasPrefix(item, "("):
				rel := Relation(strings.TrimSuffix(strings.TrimPrefix(item, "("), ")"))
				if all || d.Relation != "" || !slices.Contains(relations, rel) {
					return Decl{}, fmt.Errorf("declaration %q: bad relation %q", s, item)
				}
				d.Relation = rel
			case all:
				d.AllOf = append(d.AllOf, Key(item))
			default:
				d.AnyOf = append(d.AnyOf, Key(item))
			}
		}
	}
	return d, nil
}

// Problems lists what is wrong with the declaration against the catalogue: unknown keys, wildcards, a key the scope
// source cannot check (spec §3: a key is checked only at a scope type it may be granted at, an event key at platform
// scope only when it is platform-grantable), and incomplete declarations.
func (d Decl) Problems() []string {
	var out []string
	if !d.Scope.valid() {
		out = append(out, fmt.Sprintf("unknown scope source %q", d.Scope))
		return out
	}
	kind := d.Scope.Kind()
	credential := kind == "service" || kind == "bearer"
	if len(d.AllOf) == 0 && len(d.AnyOf) == 0 && d.Relation == "" && kind != "public" && !credential {
		out = append(out, "declares neither a key nor a relation")
	}
	if credential && (len(d.AllOf) > 0 || len(d.AnyOf) > 0 || d.Relation != "") {
		out = append(out, fmt.Sprintf("%s is authorized by its own credential: it declares no keys and no relation", d.Scope))
	}
	seenData := map[Key]bool{}
	for _, k := range d.Data {
		if seenData[k] {
			out = append(out, fmt.Sprintf("data key %s named twice", k))
		}
		seenData[k] = true
		if _, ok := Lookup(k); !ok {
			out = append(out, fmt.Sprintf("data key %s is not in the catalogue", k))
		}
	}
	if d.Relation != "" {
		if !slices.Contains(relations, d.Relation) {
			out = append(out, fmt.Sprintf("unknown relation %q", d.Relation))
		}
		if kind == "public" || kind == "self" || kind == "platform" || credential {
			out = append(out, fmt.Sprintf("relation %q needs an object or an event, not %s", d.Relation, d.Scope))
		}
	}
	seen := map[Key]bool{}
	for _, k := range append(slices.Clone(d.AllOf), d.AnyOf...) {
		if seen[k] {
			out = append(out, fmt.Sprintf("key %s named twice", k))
		}
		seen[k] = true
		spec, ok := Lookup(k)
		if !ok {
			out = append(out, fmt.Sprintf("key %s is not in the catalogue", k))
			continue
		}
		if !sourceAllows(kind, spec) {
			out = append(out, fmt.Sprintf("key %s (scopes %v) cannot be checked at %s", k, spec.Scopes, d.Scope))
		}
	}
	return out
}

func sourceAllows(kind string, spec KeySpec) bool {
	switch kind {
	case "platform":
		return spec.Allows(ScopePlatform) || spec.Allows(ScopeEvent) && spec.PlatformGrantable
	case "event", "own-team", "tenant", "object":
		// A platform grant holds in every event, so platform keys check at an event too.
		return spec.Allows(ScopeEvent) || spec.Allows(ScopePlatform) || spec.Allows(ScopeImplicit)
	case "self":
		return spec.Allows(ScopeImplicit)
	case "public":
		return spec.Allows(ScopePublic)
	}
	return false
}
