package authz

import (
	"slices"

	eventManagerModel "github.com/cybericebox/backend/internal/model/eventManager"
	"github.com/cybericebox/backend/internal/model/rbac"
)

// LegacyCheck names a check of today's code that decides a key (spec §2.5, §7.1). The shadow comparison of P2 hooks
// into these checks; this file goes in P11.
type LegacyCheck string

const (
	// CheckRequireRead: event.requireRead → uc/eventManager.RequireRead: any event membership, or platform events.read.
	CheckRequireRead LegacyCheck = "requireRead"
	// CheckRequireManage: event.requireManage → RequireManage: an owner or manager membership (plus "not archived").
	CheckRequireManage LegacyCheck = "requireManage"
	// CheckRequireExport: event.requireExport (h/event/handler.go): owner or manager membership, or events.write.
	CheckRequireExport LegacyCheck = "requireExport"
	// CheckAnalyticsSections / Sensitive / People: model/eventAnalytics.ResolveAccess levels (sections: any membership
	// or events.read; sensitive: owner or manager, or events.write; people: owner or manager, events.read or
	// events.write). The integrity reviews (uc/eventAnalytics/integrity.go) use the sensitive level.
	CheckAnalyticsSections  LegacyCheck = "eventAnalytics.ResolveAccess.Sections"
	CheckAnalyticsSensitive LegacyCheck = "eventAnalytics.ResolveAccess.Sensitive"
	CheckAnalyticsPeople    LegacyCheck = "eventAnalytics.ResolveAccess.People"
	// CheckPersonalData: http/middleware.SeesEventPersonalData: owner or manager membership, or events.read.
	CheckPersonalData LegacyCheck = "SeesEventPersonalData"
	// CheckAuthorizeExerciseRead / Manage / Flags: uc/exercise.AuthorizeExercise: the action's platform permission
	// gives everything; otherwise any member of the owner event reads, owner and manager do the rest and see flags.
	CheckAuthorizeExerciseRead   LegacyCheck = "AuthorizeExercise.Read"
	CheckAuthorizeExerciseManage LegacyCheck = "AuthorizeExercise.Manage"
	CheckAuthorizeExerciseFlags  LegacyCheck = "AuthorizeExercise.Flags"
	// CheckExportExercise: uc/exercise export: exercises.export, or a task of an event the caller owns or manages.
	CheckExportExercise LegacyCheck = "AuthorizeExport"
	// CheckRequireTestLabAuthor: uc/exercise.RequireTestLabAuthor: exercises.write, or owner or manager of an event
	// with infrastructure.
	CheckRequireTestLabAuthor LegacyCheck = "RequireTestLabAuthor"
	// CheckRequireAuthor: uc/exercise.RequireAuthor (the assistant): exercises.write, or owner or manager of any event.
	CheckRequireAuthor LegacyCheck = "RequireAuthor"
	// CheckAuthorizeTestDeploy: uc/exercise.AuthorizeTestDeploy (grader and opponent runs): write access to the task.
	CheckAuthorizeTestDeploy LegacyCheck = "AuthorizeTestDeploy"
	// CheckNew: nothing decides it today; only super_admin ("*") would hold it. Its routes are new (A.3.14).
	CheckNew LegacyCheck = "new"
)

// LegacyChecks lists every named check.
func LegacyChecks() []LegacyCheck {
	return []LegacyCheck{
		CheckRequireRead, CheckRequireManage, CheckRequireExport, CheckAnalyticsSections, CheckAnalyticsSensitive,
		CheckAnalyticsPeople, CheckPersonalData, CheckAuthorizeExerciseRead, CheckAuthorizeExerciseManage,
		CheckAuthorizeExerciseFlags, CheckExportExercise, CheckRequireTestLabAuthor, CheckRequireAuthor,
		CheckAuthorizeTestDeploy, CheckNew,
	}
}

var (
	anyEventMember  = []eventManagerModel.Role{eventManagerModel.RoleOwner, eventManagerModel.RoleManager, eventManagerModel.RoleViewer}
	eventOwnerOrMod = []eventManagerModel.Role{eventManagerModel.RoleOwner, eventManagerModel.RoleManager}
)

// EventRoles are today's event roles (event_managers.role) that pass the check at their own event.
func (c LegacyCheck) EventRoles() []eventManagerModel.Role {
	switch c {
	case CheckRequireRead, CheckAnalyticsSections, CheckAuthorizeExerciseRead:
		return slices.Clone(anyEventMember)
	case CheckRequireManage, CheckRequireExport, CheckAnalyticsSensitive, CheckAnalyticsPeople, CheckPersonalData,
		CheckAuthorizeExerciseManage, CheckAuthorizeExerciseFlags, CheckExportExercise, CheckRequireTestLabAuthor:
		return slices.Clone(eventOwnerOrMod)
	default:
		return nil
	}
}

// Legacy is what decides a key today: the platform permissions that pass (any of them; a platform permission
// inside an event check is today's implicit platform bypass, A.4.1 item 9) and the named check, either or both.
type Legacy struct {
	Permissions []rbac.Permission
	Check       LegacyCheck
	// CarriedBy: platform keys that carry today's platform reach of an event key on the same routes (any-of in
	// Appendix A.3), so the event key itself need not be platform-grantable.
	CarriedBy []Key
}

func perm(check LegacyCheck, ps ...rbac.Permission) Legacy {
	return Legacy{Permissions: ps, Check: check}
}

func (l Legacy) carriedBy(keys ...Key) Legacy {
	l.CarriedBy = keys
	return l
}

var legacy = map[Key]Legacy{
	Self:              perm("", rbac.PermSelf),
	NotificationsSelf: perm("", rbac.PermNotificationsSelf),
	BannersView:       perm("", rbac.PermBannersView),

	UsersList:          perm("", rbac.PermUsersRead),
	UsersRead:          perm("", rbac.PermUsersRead),
	UsersInvite:        perm("", rbac.PermUsersInvite),
	UsersStatusUpdate:  perm("", rbac.PermUsersStatusWrite),
	UsersDelete:        perm("", rbac.PermUsersDelete),
	RolesManage:        perm(CheckNew),
	AccessGroupsManage: perm(CheckNew),
	AccessGrantsRead:   perm("", rbac.PermUsersRoleWrite),
	AccessGrantsCreate: perm("", rbac.PermUsersRoleWrite),
	AccessGrantsDelete: perm("", rbac.PermUsersRoleWrite),
	AccessAuditRead:    perm("", rbac.PermPlatformAuditRead),

	PlatformSettingsRead:   perm("", rbac.PermPlatformSettingsRead),
	PlatformSettingsUpdate: perm("", rbac.PermPlatformSettingsWrite),
	PlatformSettingsDelete: perm("", rbac.PermPlatformSettingsWrite),
	PlatformAuditRead:      perm("", rbac.PermPlatformAuditRead),
	PlatformErrorsRead:     perm("", rbac.PermPlatformErrorsRead),
	PlatformErrorsUpdate:   perm("", rbac.PermPlatformErrorsWrite),

	NotificationsTemplatesRead:   perm("", rbac.PermNotificationsTemplatesRead),
	NotificationsTemplatesUpdate: perm("", rbac.PermNotificationsTemplatesWrite),
	NotificationsTemplatesDelete: perm("", rbac.PermNotificationsTemplatesWrite),
	NotificationsSettingsRead:    perm("", rbac.PermNotificationsSettingsRead),
	NotificationsSettingsUpdate:  perm("", rbac.PermNotificationsSettingsWrite),
	NotificationsTest:            perm("", rbac.PermNotificationsTest),
	NotificationsBroadcast:       perm("", rbac.PermNotificationsBroadcast),
	NotificationsBroadcastDelete: perm("", rbac.PermNotificationsBroadcast),
	NotificationsBannersRead:     perm("", rbac.PermNotificationsBannersRead),
	NotificationsBannersUpdate:   perm("", rbac.PermNotificationsBannersWrite),
	NotificationsBannersDelete:   perm("", rbac.PermNotificationsBannersWrite),

	AnalyticsRead:      perm("", rbac.PermAnalyticsRead),
	AnalyticsUsersRead: perm("", rbac.PermAnalyticsUsersRead),

	InfrastructureRead:   perm("", rbac.PermInfrastructureRead),
	InfrastructureUpdate: perm("", rbac.PermInfrastructureWrite),
	InfrastructureDelete: perm("", rbac.PermInfrastructureWrite),

	EventsList:              perm("", rbac.PermEventsRead),
	EventsRead:              perm("", rbac.PermEventsRead),
	EventsCreate:            perm("", rbac.PermEventsWrite),
	EventsSettingsUpdate:    perm("", rbac.PermEventsWrite),
	EventsStaffUpdate:       perm("", rbac.PermEventsWrite),
	EventsStaffDelete:       perm("", rbac.PermEventsWrite),
	EventsOwnerTransfer:     perm(CheckNew),
	EventsStatsSourceUpdate: perm("", rbac.PermPlatformSettingsWrite),
	EventsArchive:           perm("", rbac.PermEventsWrite),
	EventsArchivedRead:      perm("", rbac.PermEventsRead),
	EventsArchivedUpdate:    perm("", rbac.PermEventsWrite),
	EventsDelete:            perm("", rbac.PermEventsWrite),

	ExercisesList:             perm(CheckAuthorizeExerciseRead, rbac.PermExercisesRead),
	ExercisesRead:             perm(CheckAuthorizeExerciseRead, rbac.PermExercisesRead),
	ExercisesAllRead:          perm("", rbac.PermExercisesRead),
	ExercisesCreate:           perm("", rbac.PermExercisesWrite),
	ExercisesUpdate:           perm(CheckAuthorizeExerciseManage, rbac.PermExercisesWrite),
	ExercisesDelete:           perm(CheckAuthorizeExerciseManage, rbac.PermExercisesDelete),
	ExercisesPublish:          perm("", rbac.PermExercisesPublish),
	ExercisesFlagsRead:        perm(CheckAuthorizeExerciseFlags, rbac.PermExercisesRead),
	ExercisesExport:           perm(CheckExportExercise, rbac.PermExercisesExport),
	ExercisesSecretsRead:      perm(CheckExportExercise, rbac.PermExercisesExport),
	ExercisesAudiencesUpdate:  perm("", rbac.PermExercisesWrite),
	ExercisesLink:             perm(CheckNew),
	ExercisesTestlabStart:     perm(CheckRequireTestLabAuthor, rbac.PermExercisesWrite),
	ExercisesElevationsRead:   perm("", rbac.PermExercisesElevationsRead),
	ExercisesElevationsUpdate: perm("", rbac.PermExercisesElevationsWrite),

	CyberbrainChatUse:        perm(CheckRequireAuthor),
	CyberbrainSessionsRead:   perm(CheckRequireAuthor),
	CyberbrainRunsStart:      perm(CheckAuthorizeTestDeploy),
	CyberbrainRunsRead:       perm(CheckAuthorizeTestDeploy),
	CyberbrainStatsRead:      perm("", rbac.PermPlatformSettingsRead),
	CyberbrainStatsUpdate:    perm("", rbac.PermPlatformSettingsWrite),
	CyberbrainSettingsRead:   perm("", rbac.PermPlatformSettingsRead),
	CyberbrainSettingsUpdate: perm("", rbac.PermPlatformSettingsWrite),

	EventRead:                 perm(CheckRequireRead, rbac.PermEventsRead),
	EventSettingsRead:         perm(CheckRequireRead, rbac.PermEventsRead),
	EventSettingsUpdate:       perm(CheckRequireManage),
	EventPagesRead:            perm(CheckRequireRead, rbac.PermEventsRead),
	EventPagesUpdate:          perm(CheckRequireManage),
	EventPagesDelete:          perm(CheckRequireManage),
	EventParticipantsList:     perm(CheckRequireRead, rbac.PermEventsRead),
	EventParticipantsRead:     perm(CheckRequireRead, rbac.PermEventsRead),
	EventParticipantsUpdate:   perm(CheckRequireManage, rbac.PermEventsWrite),
	EventParticipantsDelete:   perm(CheckRequireManage, rbac.PermEventsWrite),
	EventPersonalDataRead:     perm(CheckPersonalData, rbac.PermEventsRead),
	EventTeamsRead:            perm(CheckRequireRead, rbac.PermEventsRead),
	EventTeamsUpdate:          perm(CheckRequireManage),
	EventTeamsDelete:          perm(CheckRequireManage),
	EventFormsRead:            perm(CheckRequireRead, rbac.PermEventsRead),
	EventFormsUpdate:          perm(CheckRequireManage),
	EventTasksList:            perm(CheckRequireRead, rbac.PermEventsRead),
	EventTasksRead:            perm(CheckRequireRead, rbac.PermEventsRead),
	EventTasksUpdate:          perm(CheckRequireManage),
	EventTasksDelete:          perm(CheckRequireManage),
	EventTasksExport:          perm(CheckExportExercise, rbac.PermExercisesExport).carriedBy(ExercisesExport),
	EventExercisesList:        perm(CheckRequireRead, rbac.PermEventsRead),
	EventExercisesRead:        perm(CheckRequireRead, rbac.PermEventsRead),
	EventExercisesCreate:      perm(CheckAuthorizeExerciseManage, rbac.PermExercisesWrite).carriedBy(ExercisesCreate),
	EventExercisesUpdate:      perm(CheckAuthorizeExerciseManage, rbac.PermExercisesWrite).carriedBy(ExercisesUpdate),
	EventExercisesDelete:      perm(CheckAuthorizeExerciseManage, rbac.PermExercisesDelete).carriedBy(ExercisesDelete),
	EventExercisesPublish:     perm(CheckAuthorizeExerciseManage),
	EventFlagsRead:            perm(CheckAuthorizeExerciseFlags, rbac.PermExercisesRead),
	EventAttemptsRead:         perm(CheckRequireRead, rbac.PermEventsRead, rbac.PermEventsSolutionAttemptsRead),
	EventAttemptsAnswersRead:  perm(CheckRequireManage, rbac.PermEventsSolutionAttemptsRead),
	EventAttemptsUpdate:       perm(CheckRequireManage, rbac.PermEventsSolutionAttemptsWrite),
	EventScoreboardRead:       perm(CheckRequireRead, rbac.PermEventsRead),
	EventScoreboardUpdate:     perm(CheckRequireManage),
	EventLabsRead:             perm(CheckRequireRead, rbac.PermEventsRead),
	EventLabsUpdate:           perm(CheckRequireManage),
	EventLabsModeratorTeamUse: perm(CheckRequireManage),
	EventRegistryImagesRead:   perm(CheckRequireRead, rbac.PermEventsRead),
	EventRegistryRead:         perm(CheckRequireManage),
	EventRegistryUpdate:       perm(CheckRequireManage),
	EventRegistryDelete:       perm(CheckRequireManage),
	EventCommsRead:            perm(CheckRequireRead, rbac.PermEventsRead),
	EventCommsUpdate:          perm(CheckRequireManage),
	EventCommsDelete:          perm(CheckRequireManage),
	EventStatsRead:            perm(CheckAnalyticsSections, rbac.PermEventsRead),
	EventStatsSensitiveRead:   perm(CheckAnalyticsSensitive, rbac.PermEventsWrite),
	EventStatsSensitiveUpdate: perm(CheckAnalyticsSensitive, rbac.PermEventsWrite),
	EventStatsPeopleRead:      perm(CheckAnalyticsPeople, rbac.PermEventsRead),
	EventExport:               perm(CheckRequireExport, rbac.PermEventsWrite),
	EventStaffRead:            perm("", rbac.PermEventsRead).carriedBy(EventsRead),
	EventStaffUpdate:          perm("", rbac.PermEventsWrite).carriedBy(EventsStaffUpdate),
	EventStaffDelete:          perm("", rbac.PermEventsWrite).carriedBy(EventsStaffDelete),
	EventOwnerTransfer:        perm(CheckNew),
	EventPreviewParticipant:   perm(CheckRequireManage),
	EventAccessAuditRead:      perm(CheckNew),
	EventExercisesRequest:     perm(CheckNew),
	EventArchive:              perm("", rbac.PermEventsWrite).carriedBy(EventsArchive),
	EventArchivedRead:         perm(CheckRequireRead, rbac.PermEventsRead),
}

// LegacyOf returns what decides key k today.
func LegacyOf(k Key) (Legacy, bool) {
	l, ok := legacy[k]
	l.Permissions = slices.Clone(l.Permissions)
	l.CarriedBy = slices.Clone(l.CarriedBy)
	return l, ok
}

// KeysForLegacyPermission returns the catalogue keys today's permission p decides, in catalogue order.
func KeysForLegacyPermission(p string) []Key {
	var out []Key
	for _, s := range catalogue {
		if slices.Contains(legacy[s.Key].Permissions, rbac.Permission(p)) {
			out = append(out, s.Key)
		}
	}
	return out
}
