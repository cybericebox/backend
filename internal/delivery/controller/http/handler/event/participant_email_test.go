package event_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofrs/uuid"

	eventManagerModel "github.com/cybericebox/backend/internal/model/eventManager"
	"github.com/cybericebox/backend/internal/model/rbac"
	eventUseCase "github.com/cybericebox/backend/internal/useCase/event"
)

const participantEmail = "olena@example.com"

type participantEmailUC struct{ *accessContractUC }

func (participantEmailUC) ListParticipants(context.Context, eventUseCase.ListParticipantsFilter) (eventUseCase.ParticipantsListResult, error) {
	return eventUseCase.ParticipantsListResult{Participants: []eventUseCase.ParticipantView{{UserID: uuid.Must(uuid.NewV7()), Email: participantEmail}}, Total: 1}, nil
}

func (participantEmailUC) ListParticipantsTable(context.Context, eventUseCase.ParticipantsTableQuery) (eventUseCase.ParticipantsTableResult, error) {
	return eventUseCase.ParticipantsTableResult{Participants: []eventUseCase.ParticipantView{{UserID: uuid.Must(uuid.NewV7()), Email: participantEmail}}, Total: 1, Page: 1, PageSize: 10}, nil
}

func (participantEmailUC) GetParticipantDetail(_ context.Context, _, userID uuid.UUID) (eventUseCase.ParticipantDetailView, error) {
	return eventUseCase.ParticipantDetailView{ParticipantView: eventUseCase.ParticipantView{UserID: userID, Email: participantEmail}}, nil
}

// The participants screens follow the personal-data decision of 2026-10-19, as mail and broadcasts do: the owner,
// write moderators and platform staff see the addresses in full, a read-only moderator (observer) sees them masked.
func TestParticipantEmailsAreMaskedForObservers(t *testing.T) {
	actor, eventID, userID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	forbidden := eventManagerModel.ErrEventManagementForbidden.Err()
	base := "/api/events/" + eventID.String() + "/manage/participants"
	for _, route := range []struct{ name, url string }{
		{"list", base},
		{"table", base + "?page=1"},
		{"detail", base + "/" + userID.String()},
	} {
		for _, tc := range []struct {
			name      string
			role      rbac.Role
			manageErr error
			want      string
		}{
			{name: "write moderator", role: rbac.RoleUser, want: participantEmail},
			{name: "observer", role: rbac.RoleUser, manageErr: forbidden, want: "o***@example.com"},
			{name: "admin viewer", role: rbac.RoleAdminViewer, manageErr: forbidden, want: participantEmail},
		} {
			t.Run(route.name+" "+tc.name, func(t *testing.T) {
				u := participantEmailUC{&accessContractUC{manageErr: tc.manageErr}}
				w := httptest.NewRecorder()
				routerAs(u, actor, tc.role).ServeHTTP(w, httptest.NewRequest(http.MethodGet, route.url, nil))
				if w.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
				var body struct {
					Data struct {
						Email string
						Items []struct{ Email string }
					}
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatalf("response=%s error=%v", w.Body.String(), err)
				}
				got := body.Data.Email
				if route.name != "detail" {
					if len(body.Data.Items) != 1 {
						t.Fatalf("response=%s", w.Body.String())
					}
					got = body.Data.Items[0].Email
				}
				if got != tc.want {
					t.Fatalf("email=%q, want %q", got, tc.want)
				}
			})
		}
	}
}
