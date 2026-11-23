package event_test

import (
	"context"
	"errors"
	"io"
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
	f.q.EXPECT().GetEventAnswerFileUsage(gomock.Any(), gomock.Any()).Return(postgres.GetEventAnswerFileUsageRow{}, nil).Times(2) // checked, then rechecked under the lock
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
	limits := event.AnswerFileLimits{PendingPerField: 5, PendingBytesPerEvent: 100, PendingBytesPerUser: 200}
	event.ConfigureAnswerFileLimits(limits)
	t.Cleanup(func() { event.ConfigureAnswerFileLimits(event.DefaultAnswerFileLimits()) })
	for _, tc := range []struct {
		name                      string
		pending, bytes, elsewhere int64
		want                      error
	}{
		{name: "five uploads already wait in the question", pending: 5, want: eventModel.ErrAnswerFilePendingLimit.Err()},
		{name: "the file does not fit the event budget", pending: 1, bytes: 95, want: eventModel.ErrAnswerFileQuotaExceeded.Err()},
		{name: "the file does not fit the budget of all events", pending: 1, bytes: 10, elsewhere: 185, want: eventModel.ErrAnswerFileQuotaExceeded.Err()},
		{name: "under both caps", pending: 4, bytes: 80},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAnswerFileFixture(t)
			f.q.EXPECT().GetEventAnswerFileUsage(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, arg postgres.GetEventAnswerFileUsageParams) (postgres.GetEventAnswerFileUsageRow, error) {
				if arg.EventID != f.eventID || arg.UploadedBy != f.userID || arg.Scope != "participant" || arg.FieldKey != "cv" {
					t.Fatalf("usage of %+v", arg)
				}
				return postgres.GetEventAnswerFileUsageRow{PendingInField: tc.pending, EventPendingBytes: tc.bytes, UserPendingBytes: tc.bytes + tc.elsewhere}, nil
			}).MinTimes(1).MaxTimes(2) // checked before the upload, then under the lock when it is written
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

// The caps are enforced again when the row is written, under the person's lock: an upload that a parallel one
// pushed over a cap is refused there and its stored file is discarded at once.
func TestUploadOwnAnswerFileDiscardsAFileRefusedAtWriteTime(t *testing.T) {
	event.ConfigureAnswerFileLimits(event.AnswerFileLimits{PendingPerField: 5, PendingBytesPerEvent: 100, PendingBytesPerUser: 200})
	t.Cleanup(func() { event.ConfigureAnswerFileLimits(event.DefaultAnswerFileLimits()) })
	for _, tc := range []struct {
		name                      string
		pending, bytes, elsewhere int64
		want                      error
	}{
		{name: "a parallel upload filled the question", pending: 5, want: eventModel.ErrAnswerFilePendingLimit.Err()},
		{name: "a parallel upload used the budget", pending: 1, bytes: 95, want: eventModel.ErrAnswerFileQuotaExceeded.Err()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAnswerFileFixture(t)
			unit := &testUoW{}
			f.uc = event.NewEventUseCase(event.Dependencies{Repo: f.q, BrandMedia: f.media, UoW: testUnitOfWorker{repo: f.q, unit: unit}})
			gomock.InOrder(
				f.q.EXPECT().GetEventAnswerFileUsage(gomock.Any(), gomock.Any()).Return(postgres.GetEventAnswerFileUsageRow{}, nil),
				f.q.EXPECT().GetEventAnswerFileUsage(gomock.Any(), gomock.Any()).Return(postgres.GetEventAnswerFileUsageRow{PendingInField: tc.pending, EventPendingBytes: tc.bytes, UserPendingBytes: tc.bytes + tc.elsewhere}, nil),
			)
			_, err := f.uc.UploadOwnAnswerFile(context.Background(), f.eventID, f.userID, eventFormModel.AnswerScopeParticipant, "cv", "cv.pdf", strings.NewReader("%PDF-1.7\n"))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(f.media.discarded) != 1 || f.media.discarded[0] != f.media.file.ID {
				t.Fatalf("discarded = %v, want the stored file %s", f.media.discarded, f.media.file.ID)
			}
			if unit.saved {
				t.Fatal("a refused upload must not commit")
			}
		})
	}
}

// countingReader counts what the use case reads of an upload.
type countingReader struct {
	r    io.Reader
	read int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += int64(n)
	return n, err
}

// An upload past the remaining budget is refused after reading one byte past it, not the whole body.
func TestUploadOwnAnswerFileReadsNoMoreThanTheBudget(t *testing.T) {
	event.ConfigureAnswerFileLimits(event.AnswerFileLimits{PendingPerField: 5, PendingBytesPerEvent: 100, PendingBytesPerUser: 200})
	t.Cleanup(func() { event.ConfigureAnswerFileLimits(event.DefaultAnswerFileLimits()) })
	f := newAnswerFileFixture(t)
	f.q.EXPECT().GetEventAnswerFileUsage(gomock.Any(), gomock.Any()).Return(postgres.GetEventAnswerFileUsageRow{PendingInField: 1, EventPendingBytes: 90, UserPendingBytes: 90}, nil)
	body := &countingReader{r: strings.NewReader("%PDF-1.7\n" + strings.Repeat("x", 500<<10))}
	_, err := f.uc.UploadOwnAnswerFile(context.Background(), f.eventID, f.userID, eventFormModel.AnswerScopeParticipant, "cv", "cv.pdf", body)
	if !errors.Is(err, eventModel.ErrAnswerFileQuotaExceeded.Err()) {
		t.Fatalf("err = %v", err)
	}
	if body.read > 11 {
		t.Fatalf("read %d bytes of the body, the budget left 10", body.read)
	}
}

// A refused upload whose file cannot be discarded still answers with the cap it went past: the collector reclaims
// the unreferenced file later, and the person needs to know why the upload was refused.
func TestUploadOwnAnswerFileReportsTheCapWhenTheDiscardFails(t *testing.T) {
	event.ConfigureAnswerFileLimits(event.AnswerFileLimits{PendingPerField: 5, PendingBytesPerEvent: 100, PendingBytesPerUser: 200})
	t.Cleanup(func() { event.ConfigureAnswerFileLimits(event.DefaultAnswerFileLimits()) })
	for _, tc := range []struct {
		name  string
		usage postgres.GetEventAnswerFileUsageRow
		want  error
	}{
		{name: "pending cap", usage: postgres.GetEventAnswerFileUsageRow{PendingInField: 5}, want: eventModel.ErrAnswerFilePendingLimit.Err()},
		{name: "byte budget", usage: postgres.GetEventAnswerFileUsageRow{PendingInField: 1, EventPendingBytes: 95, UserPendingBytes: 95}, want: eventModel.ErrAnswerFileQuotaExceeded.Err()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAnswerFileFixture(t)
			f.media.discardErr = errors.New("storage unavailable")
			gomock.InOrder(
				f.q.EXPECT().GetEventAnswerFileUsage(gomock.Any(), gomock.Any()).Return(postgres.GetEventAnswerFileUsageRow{}, nil),
				f.q.EXPECT().GetEventAnswerFileUsage(gomock.Any(), gomock.Any()).Return(tc.usage, nil),
			)
			_, err := f.uc.UploadOwnAnswerFile(context.Background(), f.eventID, f.userID, eventFormModel.AnswerScopeParticipant, "cv", "cv.pdf", strings.NewReader("%PDF-1.7\n"))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(f.media.discarded) != 1 {
				t.Fatalf("discard tried %d times", len(f.media.discarded))
			}
		})
	}
}
