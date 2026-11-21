package event

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/mock/gomock"

	"github.com/cybericebox/daemon/internal/delivery/repository/postgres"
	postgresMocks "github.com/cybericebox/daemon/internal/delivery/repository/postgres/mocks"
	eventModel "github.com/cybericebox/daemon/internal/model/event"
	labBindingModel "github.com/cybericebox/daemon/internal/model/labBinding"
)

// Before, at and after the finish: a grant is issued strictly before it and refused from the finish on. An event with no
// finish never refuses.
func TestRequireBeforeFinish(t *testing.T) {
	finish := time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC)
	for name, c := range map[string]struct {
		finish  *time.Time
		now     time.Time
		refused bool
	}{
		"before":                  {&finish, finish.Add(-time.Second), false},
		"at the finish":           {&finish, finish, true},
		"after":                   {&finish, finish.Add(time.Second), true},
		"after the buffer":        {&finish, finish.Add(labSessionFinishBuffer + time.Minute), true},
		"no finish, any time":     {nil, finish.Add(1000 * time.Hour), false},
		"no finish, far in past":  {nil, finish.Add(-1000 * time.Hour), false},
		"long before the finish":  {&finish, finish.Add(-100 * time.Hour), false},
		"one nanosecond before":   {&finish, finish.Add(-time.Nanosecond), false},
		"one nanosecond after":    {&finish, finish.Add(time.Nanosecond), true},
		"inside the grace period": {&finish, finish.Add(labSessionFinishBuffer / 2), true},
	} {
		err := requireBeforeFinish(c.finish, c.now)
		if c.refused != errors.Is(err, eventModel.ErrEventRuntimeNotOpen.Err()) || (!c.refused && err != nil) {
			t.Errorf("%s: refused=%v err=%v", name, c.refused, err)
		}
	}
}

func eventFinishingAt(finish *time.Time, manual *time.Time) postgres.Event {
	row := postgres.Event{
		LifecycleConfigured: true, JoinPolicy: int16(eventModel.JoinPolicyLockedAtStart),
		PublishAt: time.Now().Add(-72 * time.Hour), StartAt: time.Now().Add(-48 * time.Hour),
	}
	if finish != nil {
		row.FinishAt = pgtype.Timestamptz{Time: *finish, Valid: true}
	}
	if manual != nil {
		row.ManualFinishedAt = pgtype.Timestamptz{Time: *manual, Valid: true}
	}
	return row
}

// Through issueLabLink with a real event row: before the finish the grant ends at the finish plus the grace; after the
// finish (scheduled or made by a manager) nothing is signed.
func TestIssueLabLinkRefusesAfterTheEventFinish(t *testing.T) {
	future, past := time.Now().Add(2*time.Hour), time.Now().Add(-time.Minute)
	for name, c := range map[string]struct {
		finish, manual *time.Time
		refused        bool
	}{
		"scheduled finish ahead":               {finish: &future},
		"scheduled finish passed":              {finish: &past, refused: true},
		"manager finished it already":          {finish: &future, manual: &past, refused: true},
		"manager finish still ahead":           {finish: &future, manual: &future},
		"no finish scheduled, none made":       {},
		"no finish scheduled, made by manager": {manual: &past, refused: true},
	} {
		ctrl := gomock.NewController(t)
		q := postgresMocks.NewMockQuerier(ctrl)
		q.EXPECT().GetEventByID(gomock.Any(), gomock.Any()).Return(eventFinishingAt(c.finish, c.manual), nil).AnyTimes()
		issuer := &linkIssuer{}
		uc := NewEventUseCase(Dependencies{Repo: q, Infra: &clientInfra{}, LabSessions: issuer})
		eventID, userID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
		binding := labBindingModel.Binding{LabGroupName: "e-x-t-y", LabName: "c-1"}

		link, err := uc.issueLabLink(context.Background(), eventID, userID, binding, "web", 80)
		if c.refused {
			if !errors.Is(err, eventModel.ErrEventRuntimeNotOpen.Err()) || len(issuer.got) != 0 {
				t.Errorf("%s: err=%v signed=%d, want refused and nothing signed", name, err, len(issuer.got))
			}
			continue
		}
		if err != nil || len(issuer.got) != 1 {
			t.Errorf("%s: err=%v signed=%d", name, err, len(issuer.got))
			continue
		}
		wantEnd := time.Time{} // no finish: the issuer's fallback applies
		if f := c.finish; f != nil && c.manual == nil {
			wantEnd = f.Add(labSessionFinishBuffer)
		} else if c.manual != nil {
			wantEnd = c.manual.Add(labSessionFinishBuffer)
		}
		if !link.ExpiresAt.Equal(wantEnd) {
			t.Errorf("%s: the grant ends at %v, want %v", name, link.ExpiresAt, wantEnd)
		}
	}
}
