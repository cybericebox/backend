package event_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/jackc/pgx/v5"
	"go.uber.org/mock/gomock"

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	postgresMocks "github.com/cybericebox/backend/internal/delivery/repository/postgres/mocks"
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
	f.q.EXPECT().CreateEventAnswerFile(gomock.Any(), gomock.Any()).Return(nil)
	if _, err := f.uc.UploadOwnAnswerFile(context.Background(), f.eventID, f.userID, eventFormModel.AnswerScopeParticipant, "cv", "cv.pdf", strings.NewReader("%PDF-1.7\n")); err != nil {
		t.Fatalf("upload: %v", err)
	}
}
