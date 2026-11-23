package authz

import (
	"slices"
	"strings"
)

// RoleName names a role. Built-in roles are defined here and are read-only; custom roles live in the database (§4.4).
type RoleName string

// The built-in roles of spec §4.4. Participation has no roles (round 3).
const (
	SuperAdmin     RoleName = "super_admin"
	Admin          RoleName = "admin"
	AdminViewer    RoleName = "admin_viewer"
	User           RoleName = "user"
	Public         RoleName = "public"
	CatalogAuthor  RoleName = "catalog_author"
	CatalogReader  RoleName = "catalog_reader"
	CatalogEditor  RoleName = "catalog_editor"
	CatalogManager RoleName = "catalog_manager"
	Sysadmin       RoleName = "sysadmin"
	CyberbrainUser RoleName = "cyberbrain_user"

	EventOwner          RoleName = "event_owner"
	EventManager        RoleName = "event_manager"
	EventModerator      RoleName = "event_moderator"
	EventObserver       RoleName = "event_observer"
	EventTaskAuthor     RoleName = "event_task_author"
	EventAnalyst        RoleName = "event_analyst"
	EventContent        RoleName = "event_content"
	EventTeams          RoleName = "event_teams"
	EventLabOperator    RoleName = "event_lab_operator"
	EventCommunications RoleName = "event_communications"
)

// Role is a named set of keys, applied whole; several roles add up (§3). Scope is the scope type a grant of the role
// sits at: platform or event for grantable roles, implicit for user (every signed-in user), public for public
// (everyone, signed in or not). Implicit and public roles are never granted.
type Role struct {
	Name        RoleName
	Description string
	Scope       ScopeType
	Keys        []Key
	// covered is the set of catalogue keys Keys cover, precomputed by NewRole. A Role written as a literal has none
	// and matches its patterns on each call. Keys of a role built by NewRole are not to be changed: build a new role.
	covered map[Key]struct{}
}

// NewRole builds a role with the set of catalogue keys it covers precomputed.
func NewRole(name RoleName, description string, scope ScopeType, keys []Key) Role {
	r := Role{Name: name, Description: description, Scope: scope, Keys: slices.Clone(keys), covered: map[Key]struct{}{}}
	for _, s := range catalogue {
		if coversAny(keys, s.Key) {
			r.covered[s.Key] = struct{}{}
		}
	}
	return r
}

func coversAny(held []Key, k Key) bool {
	return slices.ContainsFunc(held, func(h Key) bool { return h.Covers(k) })
}

// Covers reports whether the role holds a key or pattern covering k.
func (r Role) Covers(k Key) bool {
	if r.covered != nil && !IsPattern(k) {
		_, ok := r.covered[k]
		return ok
	}
	return coversAny(r.Keys, k)
}

// Expanded returns the catalogue keys the role covers, in catalogue order.
func (r Role) Expanded() []Key {
	var out []Key
	for _, s := range catalogue {
		if r.Covers(s.Key) {
			out = append(out, s.Key)
		}
	}
	return out
}

// EffectiveAt reports whether a grant of the role at its own scope type makes k effective at scope type t (Effective).
func (r Role) EffectiveAt(k Key, t ScopeType) bool {
	return r.Covers(k) && scopeRule(r.Scope, k, t, slices.Contains(r.Keys, All))
}

// Effective reports whether a grant at scope type grant that holds the keys or patterns held makes k effective at
// scope type target (spec §5.2, before the time, event-end and second-factor rules): at exactly the grant's scope type
// when the key allows it, or, for a platform grant acting in an event, when the key is platform-grantable or the grant
// holds "*". The same rule serves a role grant and a personal key grant (held = the one key).
func Effective(held []Key, grant ScopeType, k Key, target ScopeType) bool {
	return coversAny(held, k) && scopeRule(grant, k, target, slices.Contains(held, All))
}

func scopeRule(grant ScopeType, k Key, target ScopeType, holdsAll bool) bool {
	spec, ok := Lookup(k)
	if !ok || !spec.Allows(target) {
		return false
	}
	if target == grant {
		return true
	}
	if target == ScopeImplicit || target == ScopePublic {
		// Every grantable role belongs to a signed-in user, who also holds the implicit keys; the public keys are
		// everyone's. Only "*" covers them outside user and public.
		return grant == ScopePlatform || grant == ScopeEvent
	}
	return target == ScopeEvent && grant == ScopePlatform && (spec.PlatformGrantable || holdsAll)
}

// eventKeysExcept is every in-event key ("event.*" in §4.4) except the listed ones; there is no deny, so a role that
// leaves a key out lists the rest.
func eventKeysExcept(except ...Key) []Key {
	var out []Key
	for _, s := range catalogue {
		if strings.HasPrefix(string(s.Key), "event.") && !slices.Contains(except, s.Key) {
			out = append(out, s.Key)
		}
	}
	return out
}

// observerKeys is event.read and the section .read / .list keys except the listed ones (event_observer, §4.4).
func observerKeys(except ...Key) []Key {
	var out []Key
	for _, k := range eventKeysExcept(except...) {
		if strings.HasSuffix(string(k), ".read") || strings.HasSuffix(string(k), ".list") {
			out = append(out, k)
		}
	}
	return out
}

// catalogKeys is the catalog part of exercises.*: every exercises key except the elevations, which stay with sysadmin.
var catalogKeys = []Key{
	ExercisesList, ExercisesRead, ExercisesAllRead, ExercisesCreate, ExercisesUpdate, ExercisesDelete,
	ExercisesPublish, ExercisesFlagsRead, ExercisesSecretsRead, ExercisesExport, ExercisesAudiencesUpdate,
	ExercisesLink, ExercisesTestlabStart,
}

// adminViewerEventKeys are the platform-grantable event keys today's admin_viewer reaches in every event without
// being staff; at the switch nobody loses access (owner decisions 2026-11-23, derived from the code of develop):
//   - events.read: RequireRead (uc/eventManager/access.go) admits it to every /manage read: the shell, settings,
//     pages, participants, teams, forms, tasks, the task library, the attempts journal, results, stands, image
//     availability, communications and an archived event; SeesEventPersonalData (http/middleware/personal_data.go)
//     shows full emails; eventAnalytics.ResolveAccess (model/eventAnalytics/access.go) gives the sections and the
//     per-person rows but not the sensitive sections;
//   - events.solution-attempts.read: GET /events/:id/solution-attempts returns answers and expected flags;
//   - exercises.read: AuthorizeExercise (uc/exercise/access.go) gives full access with flags to every task.
var adminViewerEventKeys = []Key{
	EventRead, EventSettingsRead, EventPagesRead, EventParticipantsList, EventParticipantsRead, EventPersonalDataRead,
	EventTeamsRead, EventFormsRead, EventTasksList, EventTasksRead, EventExercisesList, EventExercisesRead,
	EventFlagsRead, EventAttemptsRead, EventAttemptsAnswersRead, EventScoreboardRead, EventLabsRead,
	EventRegistryImagesRead, EventCommsRead, EventStatsRead, EventStatsPeopleRead, EventArchivedRead,
}

// adminEventKeys adds what events.write and events.solution-attempts.write reach: approving and rejecting participants
// (the admin event routes), deciding attempts, the CSV exports (requireExport), the sensitive analytics and the
// integrity reviews (eventAnalytics.ResolveAccess, uc/eventAnalytics/integrity.go). admin does not hold event.archive:
// it archives through events.archive (A.2, owner 2026-11-23).
var adminEventKeys = append(slices.Clone(adminViewerEventKeys),
	EventParticipantsUpdate, EventParticipantsDelete, EventAttemptsUpdate, EventStatsSensitiveRead,
	EventStatsSensitiveUpdate, EventExport,
)

func concat(parts ...[]Key) []Key { return slices.Concat(parts...) }

// roleDef is a built-in role as written: name, description, scope type, keys.
type roleDef struct {
	name        RoleName
	description string
	scope       ScopeType
	keys        []Key
}

var builtinRoles = func() []Role {
	out := make([]Role, len(builtinRoleDefs))
	for i, d := range builtinRoleDefs {
		out[i] = NewRole(d.name, d.description, d.scope, d.keys)
	}
	return out
}()

var builtinRoleDefs = []roleDef{
	{SuperAdmin, "Every key at every scope", ScopePlatform, []Key{All}},
	{Admin, "Today's admin: users, grants, every event from the platform side, the whole catalog, the audit log, and the platform-grantable event keys in every event", ScopePlatform, concat(
		[]Key{"users.*", "access.grants.*", AccessAuditRead, "events.*"},
		catalogKeys,
		[]Key{PlatformAuditRead},
		adminEventKeys,
	)},
	{AdminViewer, "Today's admin viewer: reads users, every event and every task", ScopePlatform, concat(
		[]Key{UsersList, UsersRead, EventsList, EventsRead, EventsArchivedRead, ExercisesList, ExercisesRead, ExercisesAllRead, ExercisesFlagsRead},
		adminViewerEventKeys,
	)},
	{User, "Every signed-in user: own data and own notifications", ScopeImplicit, []Key{Self, NotificationsSelf}},
	{Public, "Everyone, signed in or not: site banners", ScopePublic, []Key{BannersView}},
	{CatalogAuthor, "Creates own catalog tasks, no browsing", ScopePlatform, []Key{ExercisesCreate}},
	{CatalogReader, "Lists and reads catalog-audience tasks", ScopePlatform, []Key{ExercisesList, ExercisesRead}},
	{CatalogEditor, "Reads, changes, publishes and exports catalog-audience tasks", ScopePlatform, []Key{
		ExercisesList, ExercisesRead, ExercisesUpdate, ExercisesFlagsRead, ExercisesPublish, ExercisesExport,
	}},
	{CatalogManager, "The global catalog manager: the whole catalog, every task, links any task to any event, sees events as rows only", ScopePlatform, concat(
		catalogKeys, []Key{EventsList},
	)},
	{Sysadmin, "Infrastructure, platform settings, the error journal and resource elevations; no tasks", ScopePlatform, []Key{
		"infrastructure.*", "platform.settings.*", "platform.errors.*", "exercises.elevations.*",
	}},
	{CyberbrainUser, "Cyber Brain: chat, own sessions, grader and opponent runs", ScopePlatform, []Key{
		CyberbrainChatUse, CyberbrainSessionsRead, CyberbrainRunsStart, CyberbrainRunsRead,
	}},

	{EventOwner, "The event's one owner: every in-event key except archiving", ScopeEvent, concat(
		eventKeysExcept(EventArchive), []Key{ExercisesTestlabStart},
	)},
	{EventManager, "Full rights in the event except ownership transfer and archiving", ScopeEvent, concat(
		eventKeysExcept(EventOwnerTransfer, EventArchive), []Key{ExercisesTestlabStart},
	)},
	{EventModerator, "Today's moderator: works in every section, does not manage staff", ScopeEvent, concat(
		eventKeysExcept(EventStaffUpdate, EventStaffDelete, EventOwnerTransfer, EventAccessAuditRead, EventExercisesRequest, EventArchive),
		[]Key{ExercisesTestlabStart},
	)},
	{EventObserver, "Today's observer: reads the event without flags, answers, personal data, secret names, sensitive or per-person analytics and the task library", ScopeEvent, observerKeys(
		EventExercisesList, EventExercisesRead, EventFlagsRead, EventAttemptsAnswersRead, EventPersonalDataRead,
		EventRegistryRead, EventStatsSensitiveRead, EventStatsPeopleRead, EventAccessAuditRead,
	)},
	{EventTaskAuthor, "Creates and attaches the event's tasks", ScopeEvent, []Key{
		EventRead, EventExercisesList, EventExercisesRead, EventExercisesCreate, EventExercisesUpdate,
		EventTasksList, EventTasksRead, EventTasksUpdate, EventTasksDelete, EventRegistryImagesRead, ExercisesTestlabStart,
	}},
	{EventAnalyst, "Reads the analytics counts, also after the event is archived", ScopeEvent, []Key{
		EventRead, EventStatsRead, EventArchivedRead,
	}},
	{EventContent, "Settings, pages and forms", ScopeEvent, []Key{
		EventRead, "event.settings.*", "event.pages.*", "event.forms.*",
	}},
	{EventTeams, "Participants, their personal data and teams", ScopeEvent, []Key{
		EventRead, "event.participants.*", EventPersonalDataRead, "event.teams.*",
	}},
	{EventLabOperator, "Stands and the moderators' team", ScopeEvent, []Key{
		EventRead, "event.labs.*", EventTasksList, EventTasksRead, EventRegistryImagesRead,
	}},
	{EventCommunications, "Pages and communications, no tasks", ScopeEvent, []Key{
		EventRead, "event.pages.*", "event.comms.*",
	}},
}

// BuiltinRoles returns the built-in roles (copies of Keys; the precomputed key sets are shared and never written).
func BuiltinRoles() []Role {
	out := make([]Role, len(builtinRoles))
	for i, r := range builtinRoles {
		r.Keys = slices.Clone(r.Keys)
		out[i] = r
	}
	return out
}

// BuiltinRole returns one built-in role (a copy).
func BuiltinRole(name RoleName) (Role, bool) {
	for _, r := range builtinRoles {
		if r.Name == name {
			r.Keys = slices.Clone(r.Keys)
			return r, true
		}
	}
	return Role{}, false
}
