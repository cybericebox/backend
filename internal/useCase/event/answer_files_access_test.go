package event_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/mock/gomock"

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	postgresMocks "github.com/cybericebox/backend/internal/delivery/repository/postgres/mocks"
	eventModel "github.com/cybericebox/backend/internal/model/event"
	eventFormModel "github.com/cybericebox/backend/internal/model/eventForm"
	participantModel "github.com/cybericebox/backend/internal/model/participant"
	event "github.com/cybericebox/backend/internal/useCase/event"
)

// A participant uploads answer files under the same rules as saving the answers: the participant form is part of
// applying, so only a rejected person is refused; team fields are filled by approved participants only (creating
// a team or, as its captain, editing its fields). Nothing is stored for a refused caller.
func TestUploadOwnAnswerFileRequiresTheCallersParticipation(t *testing.T) {
	pending, rejected := int16(participantModel.StatusPending), int16(participantModel.StatusRejected)
	for _, tc := range []struct {
		name   string
		scope  eventFormModel.AnswerScope
		status *int16
		want   error
	}{
		{name: "team scope without participation", scope: eventFormModel.AnswerScopeTeam, want: participantModel.ErrParticipantNotFound.Err()},
		{name: "team scope while pending", scope: eventFormModel.AnswerScopeTeam, status: &pending, want: participantModel.ErrParticipantNotApproved.Err()},
		{name: "team scope when rejected", scope: eventFormModel.AnswerScopeTeam, status: &rejected, want: participantModel.ErrParticipantNotApproved.Err()},
		{name: "participant scope when rejected", scope: eventFormModel.AnswerScopeParticipant, status: &rejected, want: participantModel.ErrParticipantAccessForbidden.Err()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := postgresMocks.NewMockQuerier(gomock.NewController(t))
			uc := event.NewEventUseCase(event.Dependencies{Repo: q, BrandMedia: &brandMediaFake{}})
			eventID, userID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
			if tc.status == nil {
				q.EXPECT().GetEventParticipant(gomock.Any(), gomock.Any()).Return(postgres.EventParticipant{}, pgx.ErrNoRows)
			} else {
				q.EXPECT().GetEventParticipant(gomock.Any(), gomock.Any()).Return(postgres.EventParticipant{EventID: eventID, UserID: userID, Status: *tc.status}, nil)
			}
			_, err := uc.UploadOwnAnswerFile(context.Background(), eventID, userID, tc.scope, "cv", "cv.pdf", strings.NewReader("%PDF-1.7\n"))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// Someone who has not applied yet uploads the file of a participant form question: the form is part of applying.
func TestUploadOwnAnswerFileAcceptsAnApplicantsParticipantFormFile(t *testing.T) {
	f := newAnswerFileFixture(t)
	f.q.EXPECT().GetEventAnswerFileUsage(gomock.Any(), gomock.Any()).Return(postgres.GetEventAnswerFileUsageRow{}, nil)
	f.q.EXPECT().CreateEventAnswerFile(gomock.Any(), gomock.Any()).Return(nil)
	if _, err := f.uc.UploadOwnAnswerFile(context.Background(), f.eventID, f.userID, eventFormModel.AnswerScopeParticipant, "cv", "cv.pdf", strings.NewReader("%PDF-1.7\n")); err != nil {
		t.Fatalf("upload: %v", err)
	}
}

// answerFileEvent is a published event whose join period is open (or, with finishedAgo, finished that long ago);
// lockedAtStart closes joining at its start, an hour ago.
func answerFileEvent(eventID uuid.UUID, finished, lockedAtStart, published bool) postgres.Event {
	now := time.Now()
	e := postgres.Event{ID: eventID, LifecycleConfigured: true, JoinPolicy: int16(eventModel.JoinPolicyRolling),
		PublishAt: now.Add(-2 * time.Hour), StartAt: now.Add(-time.Hour), AvailableFrom: now.Add(-3 * time.Hour)}
	if !published {
		e.PublishAt, e.StartAt = now.Add(time.Hour), now.Add(2*time.Hour)
	}
	if finished {
		e.FinishAt = pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true}
	}
	if lockedAtStart {
		e.JoinPolicy = int16(eventModel.JoinPolicyLockedAtStart)
	}
	return e
}

// After the event's effective finish the answers are frozen (as SubmitParticipantForm), so nothing is uploaded for
// them; someone with no participant row uploads only while they may apply: the event is published and its join
// period is open (as join). Nothing is stored for a refused caller.
func TestUploadOwnAnswerFileFollowsTheEventSchedule(t *testing.T) {
	approved := int16(participantModel.StatusApproved)
	for _, tc := range []struct {
		name                               string
		status                             *int16
		finished, lockedAtStart, published bool
		want                               error
	}{
		{name: "participant after the finish", status: &approved, finished: true, published: true, want: participantModel.ErrParticipantFieldsLocked.Err()},
		{name: "applicant after the finish", finished: true, published: true, want: participantModel.ErrParticipantFieldsLocked.Err()},
		{name: "applicant before publication", want: participantModel.ErrRegistrationNotOpen.Err()},
		{name: "applicant once joining closed at the start", lockedAtStart: true, published: true, want: participantModel.ErrRegistrationNotOpen.Err()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := postgresMocks.NewMockQuerier(gomock.NewController(t))
			uc := event.NewEventUseCase(event.Dependencies{Repo: q, BrandMedia: &brandMediaFake{}})
			eventID, userID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
			q.EXPECT().GetEventByID(gomock.Any(), gomock.Any()).Return(answerFileEvent(eventID, tc.finished, tc.lockedAtStart, tc.published), nil).AnyTimes()
			if tc.status == nil {
				q.EXPECT().GetEventParticipant(gomock.Any(), gomock.Any()).Return(postgres.EventParticipant{}, pgx.ErrNoRows).AnyTimes()
			} else {
				q.EXPECT().GetEventParticipant(gomock.Any(), gomock.Any()).Return(postgres.EventParticipant{EventID: eventID, UserID: userID, Status: *tc.status}, nil).AnyTimes()
			}
			_, err := uc.UploadOwnAnswerFile(context.Background(), eventID, userID, eventFormModel.AnswerScopeParticipant, "cv", "cv.pdf", strings.NewReader("%PDF-1.7\n"))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// A person keeps at most MaxPendingAnswerFiles uploads of one question waiting for an answer, and all their files of
// an event together stay under the byte budget; past either cap nothing is stored.
func TestUploadOwnAnswerFileCapsWhatOnePersonStores(t *testing.T) {
	limits := event.AnswerFileLimits{PendingPerField: 5, BytesPerEvent: 100}
	event.ConfigureAnswerFileLimits(limits)
	t.Cleanup(func() { event.ConfigureAnswerFileLimits(event.DefaultAnswerFileLimits()) })
	for _, tc := range []struct {
		name           string
		pending, bytes int64
		want           error
	}{
		{name: "five uploads already wait in the question", pending: 5, want: eventModel.ErrAnswerFilePendingLimit.Err()},
		{name: "the file does not fit the budget", pending: 1, bytes: 95, want: eventModel.ErrAnswerFileQuotaExceeded.Err()},
		{name: "under both caps", pending: 4, bytes: 80},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAnswerFileFixture(t)
			f.q.EXPECT().GetEventAnswerFileUsage(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, arg postgres.GetEventAnswerFileUsageParams) (postgres.GetEventAnswerFileUsageRow, error) {
				if arg.EventID != f.eventID || arg.UploadedBy != f.userID || arg.Scope != "participant" || arg.FieldKey != "cv" {
					t.Fatalf("usage of %+v", arg)
				}
				return postgres.GetEventAnswerFileUsageRow{PendingInField: tc.pending, TotalBytes: tc.bytes}, nil
			})
			if tc.want == nil {
				f.q.EXPECT().CreateEventAnswerFile(gomock.Any(), gomock.Any()).Return(nil)
			}
			_, err := f.uc.UploadOwnAnswerFile(context.Background(), f.eventID, f.userID, eventFormModel.AnswerScopeParticipant, "cv", "cv.pdf", strings.NewReader("%PDF-1.7\n"))
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}
