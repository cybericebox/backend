package usertext

import "fmt"

// Texts of events: invitation results, roles, preview values, defaults.

// Results of one invitation entry.
const (
	InvitationFieldsInvalid      = "Некоректні значення полів анкети"
	InvitationEmailInvalid       = "Некоректна адреса електронної пошти"
	InvitationAlreadyParticipant = "Уже бере участь або подав заявку"
	InvitationStaff              = "Власники й модератори заходу не беруть участі як учасники"
	InvitationFailed             = "Не вдалося надіслати запрошення"
)

// Event roles in the instrumental case, for «assigned as ...» notices.
const (
	RoleModerator = "модератором"
	RoleObserver  = "спостерігачем"
)

// Placeholder values of the notification preview of an event.
const (
	PreviewFirstName       = "Імʼя"
	PreviewLastName        = "Прізвище"
	PreviewParticipantName = "Імʼя учасника"
	PreviewTeamName        = "Назва команди"
)

// ModeratorsTeamName is the team name shown for the moderators' own team.
const ModeratorsTeamName = "Команда модераторів"

// UngroupedChallenges is the group name of a task that is in no group (board and participation page).
const UngroupedChallenges = "Інші"

// ForkNameSuffix marks the copy of an event exercise.
const ForkNameSuffix = " (копія)"

// Defaults of the live event content.
const (
	LiveWidgetOrganizers = "Організатори"
	LiveWidgetPartners   = "Партнери"
)

// LiveDisplayName is the default name of the n-th live screen.
func LiveDisplayName(n int) string {
	return fmt.Sprintf("Екран %d", n)
}

// ContentVariableLabels are the editor labels of the event content variables.
var ContentVariableLabels = map[string]string{
	"event.name":                     "Назва заходу",
	"event.tag":                      "Тег заходу",
	"event.previewDescription":       "Короткий опис",
	"event.phase":                    "Поточний етап",
	"event.publishAt":                "Час публікації",
	"event.startAt":                  "Початок",
	"event.finishAt":                 "Завершення за розкладом",
	"event.withdrawAt":               "Закриття доступу",
	"event.manualFinishAt":           "Ручне завершення",
	"event.effectiveFinishAt":        "Фактичне завершення",
	"event.isPublished":              "Опубліковано",
	"event.isStarted":                "Розпочато",
	"event.isFinished":               "Завершено",
	"event.isWithdrawn":              "Доступ закрито",
	"event.runtimeOpen":              "Проходження відкрите",
	"event.registrationOpen":         "Реєстрація відкрита",
	"event.rosterOpen":               "Зміна команд відкрита",
	"event.participation":            "Формат участі",
	"event.registration":             "Режим реєстрації",
	"event.joinPolicy":               "Період приєднання",
	"event.maxTeamSize":              "Максимум у команді",
	"event.minTeamSize":              "Мінімум у команді",
	"event.maxTeams":                 "Кількість команд",
	"event.scoreboardVisibility":     "Видимість результатів",
	"event.participantsVisibility":   "Видимість учасників",
	"event.scoringProfile":           "Система оцінювання",
	"event.forceEventScoring":        "Єдина система балів",
	"event.teamCount":                "Усі команди",
	"event.approvedTeamCount":        "Схвалені команди",
	"event.participantCount":         "Усі учасники",
	"event.approvedParticipantCount": "Схвалені учасники",
	"event.registrationUnitCount":    "Зареєстровані одиниці",
	"event.challengeCount":           "Усі завдання",
	"event.availableChallengeCount":  "Доступні завдання",
	"event.solvedChallengeCount":     "Розвʼязані завдання",
	"event.solveCount":               "Успішні розвʼязання",
}
