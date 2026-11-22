package inboxUseCase_test

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	inboxModel "github.com/cybericebox/backend/internal/model/notification/inbox"
	notificationTypes "github.com/cybericebox/backend/internal/model/notification/types"
	inboxUseCase "github.com/cybericebox/backend/internal/useCase/notification/inbox"
	"github.com/cybericebox/backend/pkg/tools"
)

// A task set opened at the reveal timeout reaches every organizer of the event once, naming the stuck teams.
func TestRevealTimedOutGoesToTheEventOrganizersOnce(t *testing.T) {
	router, repo, notifier := registryRouter(t)
	eventID, setID, organizer := tools.NewUUIDv7(), tools.NewUUIDv7(), tools.NewUUIDv7()
	alarm := inboxUseCase.RevealTimeout{
		EventID: eventID, EventName: "CTF", EventTag: "ctf", EventExerciseID: setID, Revision: 3, TaskName: "Web",
		StuckTeams: []string{"Blue", "Red"}, Timeout: 5 * time.Minute, At: registryAt,
	}
	ref := inboxModel.LabRevealTimeoutRef(setID, 3)
	repo.EXPECT().InboxSubjectExists(gomock.Any(), ref).Return(false, nil)
	repo.EXPECT().ListEventWriteManagerUserIDs(gomock.Any(), eventID).Return([]uuid.UUID{organizer}, nil)

	require.NoError(t, router.RevealTimedOut(context.Background(), alarm))
	require.Len(t, notifier.sent, 1)
	got := notifier.sent[0]
	assert.Equal(t, notificationTypes.NotificationType(inboxModel.TypeLabRevealTimedOut), got.typ)
	assert.Equal(t, "Blue, Red", got.vars["teams"])
	assert.Equal(t, "2", got.vars["count"])
	assert.Equal(t, "5", got.vars["timeout_minutes"])
	assert.Equal(t, "Web", got.vars["task_name"])
	assert.Equal(t, "CTF", got.vars["event_name"])
	assert.Equal(t, inboxModel.CategoryRequests, got.options.Inbox.Category)
	assert.Equal(t, ref, got.options.Inbox.SubjectRef)

	repo.EXPECT().InboxSubjectExists(gomock.Any(), ref).Return(true, nil)
	require.NoError(t, router.RevealTimedOut(context.Background(), alarm))
	assert.Len(t, notifier.sent, 1, "one alarm per barrier")
}
