package platformAnalytics

import (
	"encoding/csv"

	"github.com/cybericebox/daemon/internal/model/usertext"
	platformAnalyticsUseCase "github.com/cybericebox/daemon/internal/useCase/platformAnalytics"
)

func writeUsersRegistrations(w *csv.Writer, v platformAnalyticsUseCase.UsersView) {
	_ = w.Write([]string{usertext.ColDate, usertext.MetricNewAccounts})
	for _, d := range v.Registrations {
		_ = w.Write([]string{csvDay(d.Day), csvInt(d.New)})
	}
}

func writeUsersActivity(w *csv.Writer, v platformAnalyticsUseCase.UsersView) {
	_ = w.Write([]string{usertext.ColDate, usertext.ColDAU, usertext.ColWAU})
	for _, d := range v.ActiveByDay {
		_ = w.Write([]string{csvDay(d.Day), csvInt(d.DAU), csvInt(d.WAU)})
	}
}

var usersMethodLabels = map[string]string{
	platformAnalyticsUseCase.UsersMethodPassword: usertext.MethodPassword,
	platformAnalyticsUseCase.UsersMethodGoogle:   "Google",
	platformAnalyticsUseCase.UsersMethodBoth:     usertext.MethodBoth,
	platformAnalyticsUseCase.UsersMethodNone:     usertext.MethodNone,
}

func writeUsersMethods(w *csv.Writer, v platformAnalyticsUseCase.UsersView) {
	_ = w.Write([]string{usertext.ColSignInMethod, usertext.ColAccounts, usertext.ColNewInPeriod})
	for _, m := range v.Methods {
		label := usersMethodLabels[m.Method]
		if label == "" {
			label = m.Method
		}
		_ = w.Write([]string{label, csvInt(m.Total), csvInt(m.New)})
	}
}

func writeUsersRetention(w *csv.Writer, v platformAnalyticsUseCase.UsersView) {
	_ = w.Write([]string{usertext.ColEventParticipation, usertext.ColAccounts})
	_ = w.Write([]string{usertext.ParticipationOne, csvInt(v.Retention.One)})
	_ = w.Write([]string{usertext.ParticipationTwo, csvInt(v.Retention.Two)})
	_ = w.Write([]string{usertext.ParticipationThreePlus, csvInt(v.Retention.ThreePlus)})
	_ = w.Write([]string{usertext.ParticipationNever, csvInt(v.Retention.Never)})
}

func writeUsersPeople(w *csv.Writer, rows []platformAnalyticsUseCase.UsersPersonView) {
	_ = w.Write([]string{usertext.ColFirstName, "Email", usertext.ColRole, usertext.ColEvents, usertext.ColSolveCount})
	for _, r := range rows {
		_ = w.Write([]string{csvText(r.Name), csvText(r.Email), csvText(r.Role), csvInt(r.EventsJoined), csvInt(r.Solves)})
	}
}
