// Package authz is the access model of platform/specs/access-control-design.md (docs/internal-docs): the key
// catalogue in code, wildcard matching, the built-in roles and the map to today's checks. Nothing in request handling
// uses it yet (phase P1a of the implementation plan): the old rbac gate still decides.
package authz

import "slices"

// Key is a permission "resource.action" or "resource.section.action" (spec §3). Catalogue keys are never created at
// runtime. A role may also hold a wildcard pattern over them: "*", or a dotted prefix ending in ".*" (match.go).
type Key string

// ScopeType is where a key may be granted (spec §3). Implicit keys are held by every signed-in user and public keys
// by everyone; neither is ever granted.
type ScopeType string

const (
	ScopePlatform ScopeType = "platform"
	ScopeEvent    ScopeType = "event"
	ScopeImplicit ScopeType = "implicit"
	ScopePublic   ScopeType = "public"
)

// KeySpec is one catalogue entry. The catalogue below is written with positional literals, so every entry states
// all three flags explicitly (spec §10).
type KeySpec struct {
	Key         Key
	Description string
	// Scopes are the scope types the key may be granted at; a personal key at another scope type is refused, and a
	// key inside a role granted at another scope type does nothing there.
	Scopes []ScopeType
	// PlatformGrantable: an event key that may also be granted at platform scope, meaning "in every event" (§9 Q20).
	PlatformGrantable bool
	// SurvivesEventEnd: still effective at an event that has ended or been archived (§6.5).
	SurvivesEventEnd bool
	// SecondFactor: effective only for a user with a second factor (§6.6).
	SecondFactor bool
}

// Allows reports whether the key may be granted (or, for implicit and public keys, is held) at scope type t.
func (s KeySpec) Allows(t ScopeType) bool { return slices.Contains(s.Scopes, t) }

// Grantable reports whether the key can be granted at all (platform or event scope).
func (s KeySpec) Grantable() bool { return s.Allows(ScopePlatform) || s.Allows(ScopeEvent) }

// All is the wildcard of super_admin: every key at every scope (§4.4).
const All Key = "*"

// The 128 keys of Appendix A.2, in its order.
const (
	Self                         Key = "self"
	NotificationsSelf            Key = "notifications.self"
	BannersView                  Key = "banners.view"
	UsersList                    Key = "users.list"
	UsersRead                    Key = "users.read"
	UsersInvite                  Key = "users.invite"
	UsersStatusUpdate            Key = "users.status.update"
	UsersDelete                  Key = "users.delete"
	RolesManage                  Key = "roles.manage"
	AccessGroupsManage           Key = "access.groups.manage"
	AccessGrantsRead             Key = "access.grants.read"
	AccessGrantsCreate           Key = "access.grants.create"
	AccessGrantsDelete           Key = "access.grants.delete"
	AccessAuditRead              Key = "access.audit.read"
	PlatformSettingsRead         Key = "platform.settings.read"
	PlatformSettingsUpdate       Key = "platform.settings.update"
	PlatformSettingsDelete       Key = "platform.settings.delete"
	PlatformAuditRead            Key = "platform.audit.read"
	PlatformErrorsRead           Key = "platform.errors.read"
	PlatformErrorsUpdate         Key = "platform.errors.update"
	NotificationsTemplatesRead   Key = "notifications.templates.read"
	NotificationsTemplatesUpdate Key = "notifications.templates.update"
	NotificationsTemplatesDelete Key = "notifications.templates.delete"
	NotificationsSettingsRead    Key = "notifications.settings.read"
	NotificationsSettingsUpdate  Key = "notifications.settings.update"
	NotificationsTest            Key = "notifications.test"
	NotificationsBroadcast       Key = "notifications.broadcast"
	NotificationsBroadcastDelete Key = "notifications.broadcast.delete"
	NotificationsBannersRead     Key = "notifications.banners.read"
	NotificationsBannersUpdate   Key = "notifications.banners.update"
	NotificationsBannersDelete   Key = "notifications.banners.delete"
	AnalyticsRead                Key = "analytics.read"
	AnalyticsUsersRead           Key = "analytics.users.read"
	InfrastructureRead           Key = "infrastructure.read"
	InfrastructureUpdate         Key = "infrastructure.update"
	InfrastructureDelete         Key = "infrastructure.delete"
	EventsList                   Key = "events.list"
	EventsRead                   Key = "events.read"
	EventsCreate                 Key = "events.create"
	EventsSettingsUpdate         Key = "events.settings.update"
	EventsStaffUpdate            Key = "events.staff.update"
	EventsStaffDelete            Key = "events.staff.delete"
	EventsOwnerTransfer          Key = "events.owner.transfer"
	EventsStatsSourceUpdate      Key = "events.stats_source.update"
	EventsArchive                Key = "events.archive"
	EventsArchivedRead           Key = "events.archived.read"
	EventsArchivedUpdate         Key = "events.archived.update"
	EventsDelete                 Key = "events.delete"
	ExercisesList                Key = "exercises.list"
	ExercisesRead                Key = "exercises.read"
	ExercisesAllRead             Key = "exercises.all.read"
	ExercisesCreate              Key = "exercises.create"
	ExercisesUpdate              Key = "exercises.update"
	ExercisesDelete              Key = "exercises.delete"
	ExercisesPublish             Key = "exercises.publish"
	ExercisesFlagsRead           Key = "exercises.flags.read"
	ExercisesExport              Key = "exercises.export"
	ExercisesSecretsRead         Key = "exercises.secrets.read"
	ExercisesAudiencesUpdate     Key = "exercises.audiences.update"
	ExercisesLink                Key = "exercises.link"
	ExercisesTestlabStart        Key = "exercises.testlab.start"
	ExercisesElevationsRead      Key = "exercises.elevations.read"
	ExercisesElevationsUpdate    Key = "exercises.elevations.update"
	CyberbrainChatUse            Key = "cyberbrain.chat.use"
	CyberbrainSessionsRead       Key = "cyberbrain.sessions.read"
	CyberbrainRunsStart          Key = "cyberbrain.runs.start"
	CyberbrainRunsRead           Key = "cyberbrain.runs.read"
	CyberbrainStatsRead          Key = "cyberbrain.stats.read"
	CyberbrainStatsUpdate        Key = "cyberbrain.stats.update"
	CyberbrainSettingsRead       Key = "cyberbrain.settings.read"
	CyberbrainSettingsUpdate     Key = "cyberbrain.settings.update"
	EventRead                    Key = "event.read"
	EventSettingsRead            Key = "event.settings.read"
	EventSettingsUpdate          Key = "event.settings.update"
	EventPagesRead               Key = "event.pages.read"
	EventPagesUpdate             Key = "event.pages.update"
	EventPagesDelete             Key = "event.pages.delete"
	EventParticipantsList        Key = "event.participants.list"
	EventParticipantsRead        Key = "event.participants.read"
	EventParticipantsUpdate      Key = "event.participants.update"
	EventParticipantsDelete      Key = "event.participants.delete"
	EventPersonalDataRead        Key = "event.personal-data.read"
	EventTeamsRead               Key = "event.teams.read"
	EventTeamsUpdate             Key = "event.teams.update"
	EventTeamsDelete             Key = "event.teams.delete"
	EventFormsRead               Key = "event.forms.read"
	EventFormsUpdate             Key = "event.forms.update"
	EventTasksList               Key = "event.tasks.list"
	EventTasksRead               Key = "event.tasks.read"
	EventTasksUpdate             Key = "event.tasks.update"
	EventTasksDelete             Key = "event.tasks.delete"
	EventTasksExport             Key = "event.tasks.export"
	EventExercisesList           Key = "event.exercises.list"
	EventExercisesRead           Key = "event.exercises.read"
	EventExercisesCreate         Key = "event.exercises.create"
	EventExercisesUpdate         Key = "event.exercises.update"
	EventExercisesDelete         Key = "event.exercises.delete"
	EventExercisesPublish        Key = "event.exercises.publish"
	EventFlagsRead               Key = "event.flags.read"
	EventAttemptsRead            Key = "event.attempts.read"
	EventAttemptsAnswersRead     Key = "event.attempts.answers.read"
	EventAttemptsUpdate          Key = "event.attempts.update"
	EventScoreboardRead          Key = "event.scoreboard.read"
	EventScoreboardUpdate        Key = "event.scoreboard.update"
	EventLabsRead                Key = "event.labs.read"
	EventLabsUpdate              Key = "event.labs.update"
	EventLabsModeratorTeamUse    Key = "event.labs.moderator-team.use"
	EventRegistryImagesRead      Key = "event.registry.images.read"
	EventRegistryRead            Key = "event.registry.read"
	EventRegistryUpdate          Key = "event.registry.update"
	EventRegistryDelete          Key = "event.registry.delete"
	EventCommsRead               Key = "event.comms.read"
	EventCommsUpdate             Key = "event.comms.update"
	EventCommsDelete             Key = "event.comms.delete"
	EventStatsRead               Key = "event.stats.read"
	EventStatsSensitiveRead      Key = "event.stats.sensitive.read"
	EventStatsSensitiveUpdate    Key = "event.stats.sensitive.update"
	EventStatsPeopleRead         Key = "event.stats.people.read"
	EventExport                  Key = "event.export"
	EventStaffRead               Key = "event.staff.read"
	EventStaffUpdate             Key = "event.staff.update"
	EventStaffDelete             Key = "event.staff.delete"
	EventOwnerTransfer           Key = "event.owner.transfer"
	EventPreviewParticipant      Key = "event.preview.participant"
	EventAccessAuditRead         Key = "event.access.audit.read"
	EventExercisesRequest        Key = "event.exercises.request"
	EventArchive                 Key = "event.archive"
	EventArchivedRead            Key = "event.archived.read"
)

var (
	platform         = []ScopeType{ScopePlatform}
	event            = []ScopeType{ScopeEvent}
	platformAndEvent = []ScopeType{ScopePlatform, ScopeEvent}
	implicit         = []ScopeType{ScopeImplicit}
	public           = []ScopeType{ScopePublic}
)

// catalogue: key, description, scope types, platform-grantable, survives-event-end, second-factor.
var catalogue = []KeySpec{
	{Self, "Any signed-in user acting on their own data (account, own deliveries, own uploads)", implicit, false, false, false},
	{NotificationsSelf, "Own inbox, own notification settings, dismiss own banners", implicit, false, false, false},
	{BannersView, "Site banners and pulse", public, false, false, false},
	{UsersList, "User rows: name, email, role, status; facets and stats", platform, false, false, false},
	{UsersRead, "One user's card and their participations", platform, false, false, false},
	{UsersInvite, "Invite users, resend invitations", platform, false, false, false},
	{UsersStatusUpdate, "Block, unblock, end sessions (only lower accounts)", platform, false, false, false},
	{UsersDelete, "Delete an account (only lower accounts)", platform, false, false, false},
	{RolesManage, "Create, edit and archive custom roles from keys the holder holds; never a role the holder holds", platform, false, false, true},
	{AccessGroupsManage, "Create, edit and archive groups, add and remove members; never a group the holder is in", platform, false, false, true},
	{AccessGrantsRead, "See who holds which role or key at a scope", platform, false, false, true},
	{AccessGrantsCreate, "Grant a role or key to a person or a group (delegation rule, step-up)", platform, false, false, true},
	{AccessGrantsDelete, "Revoke a grant (step-up)", platform, false, false, true},
	{AccessAuditRead, "The whole grant journal", platform, false, false, true},
	{PlatformSettingsRead, "Platform settings, mail providers, image registry, limits, preview defaults", platform, false, false, false},
	{PlatformSettingsUpdate, "Change platform settings, mail providers, image registry, limits, preview defaults", platform, false, false, false},
	{PlatformSettingsDelete, "Delete mail providers", platform, false, false, false},
	{PlatformAuditRead, "Admin audit log and its export", platform, false, false, false},
	{PlatformErrorsRead, "Error journal", platform, false, false, false},
	{PlatformErrorsUpdate, "Error statuses and journal settings", platform, false, false, false},
	{NotificationsTemplatesRead, "Platform templates, types, dispatches, stats, previews", platform, false, false, false},
	{NotificationsTemplatesUpdate, "Create, change, publish and roll back templates and presets", platform, false, false, false},
	{NotificationsTemplatesDelete, "Delete templates and block presets", platform, false, false, false},
	{NotificationsSettingsRead, "Global notification settings and signal defaults", platform, false, false, false},
	{NotificationsSettingsUpdate, "Change global notification settings and signal defaults", platform, false, false, false},
	{NotificationsTest, "Send a test notification", platform, false, false, false},
	{NotificationsBroadcast, "Platform broadcasts, custom templates, resend dispatches", platform, false, false, false},
	{NotificationsBroadcastDelete, "Delete custom broadcast templates", platform, false, false, false},
	{NotificationsBannersRead, "Site banner rows", platform, false, false, false},
	{NotificationsBannersUpdate, "Create and change site banners", platform, false, false, false},
	{NotificationsBannersDelete, "Delete site banners", platform, false, false, false},
	{AnalyticsRead, "Platform analytics (counts)", platform, false, false, false},
	{AnalyticsUsersRead, "Per-person rows of platform analytics", platform, false, false, false},
	{InfrastructureRead, "Agents, calendar, monitoring, stands and test labs of the cluster", platform, false, false, false},
	{InfrastructureUpdate, "Change agents, reservations, recreate stands, terminate labs", platform, false, false, false},
	{InfrastructureDelete, "Delete agents and calendar reservations", platform, false, false, false},
	{EventsList, "Event rows: name, dates, state (all a catalog manager sees)", platform, false, false, false},
	{EventsRead, "The event's platform card: summary, config, staff list", platform, false, false, false},
	{EventsCreate, "Create an event (creator gets event_owner)", platform, false, false, false},
	{EventsSettingsUpdate, "Name, dates, config, infrastructure, theme from the admin side", platform, false, false, false},
	{EventsStaffUpdate, "Appoint staff in any event (delegation applies; staff assignment grants rights, so second factor)", platform, false, false, true},
	{EventsStaffDelete, "Remove staff in any event (delegation applies)", platform, false, false, true},
	{EventsOwnerTransfer, "Hand an event's ownership to another person from the platform side", platform, false, false, false},
	{EventsStatsSourceUpdate, "The event's checkbox \"do not collect statistics from this event\"", platform, false, false, false},
	{EventsArchive, "Archive events", platform, false, false, false},
	{EventsArchivedRead, "Archived events: their rows, their platform card and, with P keys at platform scope, their inside", platform, false, false, false},
	{EventsArchivedUpdate, "Restore an archived event", platform, false, false, false},
	{EventsDelete, "Delete events (archiving is events.archive)", platform, false, false, false},
	{ExercisesList, "Catalog-audience tasks at list level", platform, false, false, false},
	{ExercisesRead, "Catalog-audience tasks at read level", platform, false, false, false},
	{ExercisesAllRead, "Global catalog manager: with exercises.list / exercises.read, every task, invisible ones included", platform, false, false, false},
	{ExercisesCreate, "Create and import catalog tasks (own tasks only)", platform, false, false, false},
	{ExercisesUpdate, "Change visible tasks, test-deploy them", platform, false, false, false},
	{ExercisesDelete, "Delete visible tasks", platform, false, false, false},
	{ExercisesPublish, "Publish a version to the catalog, roll back; review catalog proposals", platform, false, false, false},
	{ExercisesFlagsRead, "Solutions of visible tasks", platform, false, false, false},
	{ExercisesExport, "Export visible tasks; the archive holds only what the holder may read", platform, false, false, false},
	{ExercisesSecretsRead, "Secret values of visible tasks, in the editor and in exports", platform, false, false, false},
	{ExercisesAudiencesUpdate, "Change audiences of a task the holder does not author", platform, false, false, false},
	{ExercisesLink, "Link a task the holder sees at read level to any event seen through events.list; any task with exercises.all.read", platform, false, false, false},
	{ExercisesTestlabStart, "Start a test lab, book and open test-lab time on a task the holder may update; the owner event must allow infrastructure", platformAndEvent, false, false, false},
	{ExercisesElevationsRead, "Elevation requests", platform, false, false, false},
	{ExercisesElevationsUpdate, "Approve or reject them", platform, false, false, false},
	{CyberbrainChatUse, "Cyber Brain chat: ideas, sessions, messages, attachments, start, stop, save", platform, false, false, false},
	{CyberbrainSessionsRead, "Own Cyber Brain sessions: archive, event stream", platform, false, false, false},
	{CyberbrainRunsStart, "Start and stop grader and opponent runs on a task the holder may update", platform, false, false, false},
	{CyberbrainRunsRead, "Lists, results and event streams of those runs", platform, false, false, false},
	{CyberbrainStatsRead, "Statistics sources and modes, defaults and per event", platform, false, false, false},
	{CyberbrainStatsUpdate, "Change which sources feed the statistics, and the modes", platform, false, false, false},
	{CyberbrainSettingsRead, "Cyber Brain settings, the global switch cyberbrain.enabled", platform, false, false, false},
	{CyberbrainSettingsUpdate, "Change Cyber Brain settings and the global switch cyberbrain.enabled", platform, false, false, false},
	{EventRead, "The event shell: access, readiness, stages, name", event, true, true, false},
	{EventSettingsRead, "Config, lifecycle, scoring, list columns", event, true, false, false},
	{EventSettingsUpdate, "Config, appearance, theme, lifecycle, scoring, logo, preview picture, list columns, preview settings", event, false, false, false},
	{EventPagesRead, "Landing, pages, live layout, screens, variables", event, true, false, false},
	{EventPagesUpdate, "Change and publish them; screen links (bearer secrets); the public visibility of the event pages", event, false, false, false},
	{EventPagesDelete, "Delete pages and live screens, revoke screen links", event, false, false, false},
	{EventParticipantsList, "Participant rows", event, true, false, false},
	{EventParticipantsRead, "Participant detail, staff fields, form answers, answer files", event, true, false, false},
	{EventParticipantsUpdate, "Invite, approve, move, hide; staff fields; answer files", event, true, false, false},
	{EventParticipantsDelete, "Reject participants, revoke invitations", event, true, false, false},
	{EventPersonalDataRead, "Full emails and the SMTP user (masked without it)", event, true, false, false},
	{EventTeamsRead, "Teams, members, staff fields", event, true, false, false},
	{EventTeamsUpdate, "Create and change teams; add members, captain, admission", event, false, false, false},
	{EventTeamsDelete, "Delete teams, remove members", event, false, false, false},
	{EventFormsRead, "Participant form, team fields, forms, deliveries", event, true, false, false},
	{EventFormsUpdate, "Change them, assign forms", event, false, false, false},
	{EventTasksList, "Tasks attached to the event, challenge groups", event, true, false, false},
	{EventTasksRead, "Their challenges and team copies, without flags", event, true, false, false},
	{EventTasksUpdate, "Attach, replace, fork, visibility, scoring, hints, order, stages", event, false, false, false},
	{EventTasksDelete, "Detach tasks, delete challenge groups and stages", event, false, false, false},
	{EventTasksExport, "Export the event's own tasks with flags", event, false, false, false},
	{EventExercisesList, "The event's task library (tasks whose audience includes this event)", event, true, false, false},
	{EventExercisesRead, "Read a task of the library", event, true, false, false},
	{EventExercisesCreate, "Create or import tasks owned by this event", event, false, false, false},
	{EventExercisesUpdate, "Change tasks owned by this event", event, false, false, false},
	{EventExercisesDelete, "Delete tasks owned by this event", event, false, false, false},
	{EventExercisesPublish, "Propose or publish an event-owned task to the catalog", event, false, false, false},
	{EventFlagsRead, "Flags of attached and event-owned tasks, expected flags", event, true, false, false},
	{EventAttemptsRead, "Attempts journal, live attempts, hint unlocks", event, true, false, false},
	{EventAttemptsAnswersRead, "Submitted answers and expected flags in the journal", event, true, false, false},
	{EventAttemptsUpdate, "Decide and annul attempts", event, true, false, false},
	{EventScoreboardRead, "Manager view of results, results settings", event, true, true, false},
	{EventScoreboardUpdate, "Results settings, opening results, the public visibility of the scoreboard and of the results", event, false, false, false},
	{EventLabsRead, "Stands, stand detail, resource plan and calendar", event, true, false, false},
	{EventLabsUpdate, "Stand settings, recreate, reset, rescue, change requests", event, false, false, false},
	{EventLabsModeratorTeamUse, "The moderators' team: board, labs, VPN, submit, files", event, false, false, false},
	{EventRegistryImagesRead, "Image availability of the event's tasks", event, true, false, false},
	{EventRegistryRead, "Registry secret names and status, never values", event, false, false, false},
	{EventRegistryUpdate, "Create, change and check registry secrets", event, false, false, false},
	{EventRegistryDelete, "Delete registry secrets", event, false, false, false},
	{EventCommsRead, "Banners, broadcasts, deliveries, templates, subscriptions, event mail and journal", event, true, false, false},
	{EventCommsUpdate, "Change and send them; SMTP; resets to the default", event, false, false, false},
	{EventCommsDelete, "Delete banners and templates", event, false, false, false},
	{EventStatsRead, "Analytics sections (counts)", event, true, true, false},
	{EventStatsSensitiveRead, "Wrong-answer texts, integrity signals", event, true, false, false},
	{EventStatsSensitiveUpdate, "Review solves, dismiss integrity patterns", event, true, false, false},
	{EventStatsPeopleRead, "Per-person analytics rows and emails", event, true, false, false},
	{EventExport, "CSV exports and report download", event, true, true, false},
	{EventStaffRead, "Staff of the event", event, false, true, false},
	{EventStaffUpdate, "Grant staff roles at the event; staff assignment grants rights, so second factor", event, false, false, true},
	{EventStaffDelete, "Revoke staff grants at the event", event, false, false, true},
	{EventOwnerTransfer, "Hand ownership to another person; exactly one owner", event, false, false, false},
	{EventPreviewParticipant, "Participant preview: open the event as a participant, test labs", event, false, false, false},
	{EventAccessAuditRead, "Grant journal entries of this event", event, false, true, false},
	{EventExercisesRequest, "See closed listable tasks at list level; request access for this event", event, false, false, false},
	{EventArchive, "Archive this event from the event side", event, true, false, false},
	{EventArchivedRead, "See and read this event after it is archived; event grants then count only with it, and only for E keys", event, true, true, false},
}

var byKey = func() map[Key]int {
	m := make(map[Key]int, len(catalogue))
	for i, s := range catalogue {
		m[s.Key] = i
	}
	return m
}()

// Catalogue returns every key, in catalogue order (a copy).
func Catalogue() []KeySpec {
	out := make([]KeySpec, len(catalogue))
	for i, s := range catalogue {
		s.Scopes = slices.Clone(s.Scopes)
		out[i] = s
	}
	return out
}

// Lookup returns the catalogue entry of a concrete key.
func Lookup(k Key) (KeySpec, bool) {
	i, ok := byKey[k]
	if !ok {
		return KeySpec{}, false
	}
	s := catalogue[i]
	s.Scopes = slices.Clone(s.Scopes)
	return s, true
}
