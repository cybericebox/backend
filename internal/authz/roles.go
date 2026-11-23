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
}

// Covers reports whether the role holds a key or pattern covering k.
func (r Role) Covers(k Key) bool {
	return slices.ContainsFunc(r.Keys, func(held Key) bool { return held.Covers(k) })
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

// EffectiveAt reports whether a grant of the role at its own scope type makes k effective at scope type t (spec §5.2,
// before the time, event-end and second-factor rules): at exactly that scope type when the key allows it, or, for a
// platform grant acting in an event, when the key is platform-grantable or the role holds "*".
func (r Role) EffectiveAt(k Key, t ScopeType) bool {
	spec, ok := Lookup(k)
	if !ok || !r.Covers(k) || !spec.Allows(t) {
		return false
	}
	if t == r.Scope {
		return true
	}
	if t == ScopeImplicit || t == ScopePublic {
		// Every grantable role belongs to a signed-in user, who also holds the implicit keys; the public keys are
		// everyone's. Only "*" covers them outside user and public.
		return r.Scope == ScopePlatform || r.Scope == ScopeEvent
	}
	return t == ScopeEvent && r.Scope == ScopePlatform && (spec.PlatformGrantable || slices.Contains(r.Keys, All))
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
// being staff (owner decision 2026-11-23: derived from the code of develop):
//   - events.read: RequireRead (uc/eventManager/access.go) admits it to every /manage read, so event.read,
//     participants, tasks, the task library, the attempts journal, results and an archived event;
//     SeesEventPersonalData (http/middleware/personal_data.go) shows full emails; eventAnalytics.ResolveAccess
//     (model/eventAnalytics/access.go) gives the per-person rows but not the sensitive sections;
//   - events.solution-attempts.read: GET /events/:id/solution-attempts returns answers and expected flags;
//   - exercises.read: AuthorizeExercise (uc/exercise/access.go) gives full access with flags to every task.
var adminViewerEventKeys = []Key{
	EventRead, EventParticipantsList, EventParticipantsRead, EventPersonalDataRead, EventTasksList, EventTasksRead,
	EventExercisesList, EventExercisesRead, EventFlagsRead, EventAttemptsRead, EventAttemptsAnswersRead,
	EventScoreboardRead, EventStatsPeopleRead, EventArchivedRead,
}

// adminEventKeys adds what events.write reaches: the sensitive analytics (eventAnalytics.ResolveAccess). admin does not
// hold event.archive: it archives through events.archive (A.2, owner 2026-11-23).
var adminEventKeys = append(slices.Clone(adminViewerEventKeys), EventStatsSensitiveRead)

func concat(parts ...[]Key) []Key { return slices.Concat(parts...) }

var builtinRoles = []Role{
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

// BuiltinRoles returns the built-in roles (copies).
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
