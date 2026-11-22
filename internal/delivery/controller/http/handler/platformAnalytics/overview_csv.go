package platformAnalytics

import (
	"encoding/csv"
	"sort"
	"time"

	"github.com/cybericebox/backend/internal/model/usertext"
	platformAnalyticsUseCase "github.com/cybericebox/backend/internal/useCase/platformAnalytics"
)

// csvPrevious prints the previous period's value; empty for all time.
func csvPrevious(m platformAnalyticsUseCase.OverviewMetricView) string {
	if m.Previous == nil {
		return ""
	}
	return csvInt(*m.Previous)
}

func writeOverviewSummary(w *csv.Writer, v platformAnalyticsUseCase.OverviewView) {
	_ = w.Write([]string{usertext.ColIndicator, usertext.ColValue, usertext.ColPreviousPeriod})
	metric := func(name string, m platformAnalyticsUseCase.OverviewMetricView) {
		_ = w.Write([]string{name, csvInt(m.Value), csvPrevious(m)})
	}
	current := func(name string, value int64) { _ = w.Write([]string{name, csvInt(value), ""}) }
	current(usertext.MetricAccountsTotal, v.Users.Total)
	metric(usertext.MetricNewAccounts, v.Users.New)
	metric(usertext.MetricActiveAccounts, v.Users.Active)
	current(usertext.MetricEventsDraft, v.Events.Draft)
	current(usertext.MetricEventsPublished, v.Events.Published)
	current(usertext.MetricEventsRunning, v.Events.Running)
	current(usertext.MetricEventsFinished, v.Events.Finished)
	current(usertext.MetricEventsArchived, v.Events.Archived)
	metric(usertext.MetricNewEvents, v.Events.New)
	metric(usertext.MetricParticipantRegistrations, v.Participants.Registered)
	metric(usertext.MetricApprovedParticipants, v.Participants.Approved)
	metric(usertext.ColAttemptCount, v.Activity.Attempts)
	metric(usertext.ColSolveCount, v.Activity.Solves)
	metric(usertext.ColEmailsSent, v.Mail.Sent)
	metric(usertext.ColEmailsFailed, v.Mail.Failed)
	current(usertext.MetricStandsRunning, v.Stands.Ready)
	current(usertext.MetricStandsDeploying, v.Stands.Creating)
	current(usertext.MetricStandsFailedNow, v.Stands.Failed)
	metric(usertext.MetricStandFailures, v.Stands.Failures)
}

func writeOverviewSeries(w *csv.Writer, v platformAnalyticsUseCase.OverviewView) {
	_ = w.Write([]string{usertext.ColDate, usertext.MetricNewAccounts, usertext.ColAttemptCount, usertext.ColSolveCount, usertext.ColEmailsSent, usertext.ColEmailsFailed})
	type row struct{ newUsers, attempts, solves, sent, failed int64 }
	days := map[time.Time]*row{}
	at := func(day time.Time) *row {
		if days[day] == nil {
			days[day] = &row{}
		}
		return days[day]
	}
	for _, d := range v.Series.NewUsers {
		at(d.Day).newUsers = d.New
	}
	for _, d := range v.Series.Activity {
		r := at(d.Day)
		r.attempts, r.solves = d.Attempts, d.Solves
	}
	for _, d := range v.Series.Mail {
		r := at(d.Day)
		r.sent, r.failed = d.Sent, d.Failed
	}
	keys := make([]time.Time, 0, len(days))
	for day := range days {
		keys = append(keys, day)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Before(keys[j]) })
	for _, day := range keys {
		r := days[day]
		_ = w.Write([]string{csvDay(day), csvInt(r.newUsers), csvInt(r.attempts), csvInt(r.solves), csvInt(r.sent), csvInt(r.failed)})
	}
}
