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

// personalDataUC returns one address on every manage screen that lists people and records whether the lists were
// asked to keep the addresses out of their search, filters and sorting.
type personalDataUC struct {
	*accessContractUC
	hideEmails map[string]bool
}

func (u personalDataUC) ListParticipants(_ context.Context, f eventUseCase.ListParticipantsFilter) (eventUseCase.ParticipantsListResult, error) {
	u.hideEmails["participants"] = f.HideEmails
	return eventUseCase.ParticipantsListResult{}, nil
}

func (u personalDataUC) ListParticipantsTable(_ context.Context, q eventUseCase.ParticipantsTableQuery) (eventUseCase.ParticipantsTableResult, error) {
	u.hideEmails["participants table"] = q.HideEmails
	return eventUseCase.ParticipantsTableResult{Page: 1, PageSize: 10}, nil
}

func (u personalDataUC) ListTeamsTable(_ context.Context, q eventUseCase.TeamsTableQuery) (eventUseCase.TeamsTableResult, error) {
	u.hideEmails["teams table"] = q.HideEmails
	return eventUseCase.TeamsTableResult{Teams: []eventUseCase.TeamView{personalDataTeam()}, Total: 1, Page: 1, PageSize: 10}, nil
}

func (personalDataUC) ListTeams(context.Context, eventUseCase.ListTeamsFilter) (eventUseCase.TeamsListResult, error) {
	return eventUseCase.TeamsListResult{Teams: []eventUseCase.TeamView{personalDataTeam()}, Total: 1}, nil
}

func (personalDataUC) GetTeamProfile(context.Context, uuid.UUID, uuid.UUID) (eventUseCase.TeamProfileView, error) {
	return eventUseCase.TeamProfileView{Team: personalDataTeam()}, nil
}

func (personalDataUC) ListEventFormAnswers(context.Context, uuid.UUID, uuid.UUID) ([]eventUseCase.EventFormAnswerView, error) {
	return []eventUseCase.EventFormAnswerView{{UserID: uuid.Must(uuid.NewV7()), Email: participantEmail}}, nil
}

func (personalDataUC) ListParticipantFormAnswers(context.Context, uuid.UUID) ([]eventUseCase.ParticipantFormAnswerView, error) {
	return []eventUseCase.ParticipantFormAnswerView{{UserID: uuid.Must(uuid.NewV7()), Email: participantEmail}}, nil
}

func personalDataTeam() eventUseCase.TeamView {
	return eventUseCase.TeamView{
		ID:                 uuid.Must(uuid.NewV7()),
		Members:            []eventUseCase.TeamMemberView{{UserID: uuid.Must(uuid.NewV7()), Email: participantEmail}},
		PendingInvitations: []eventUseCase.TeamInvitationView{{UserID: uuid.Must(uuid.NewV7()), Email: participantEmail}},
	}
}

// emailsIn collects every "Email" value of a JSON document, wherever it is.
func emailsIn(value any) []string {
	var out []string
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if text, ok := item.(string); ok && key == "Email" {
				out = append(out, text)
				continue
			}
			out = append(out, emailsIn(item)...)
		}
	case []any:
		for _, item := range v {
			out = append(out, emailsIn(item)...)
		}
	}
	return out
}

// The teams screens and the form answers follow the personal-data decision of 2026-10-19 as the participants
// screen does: an observer gets the members', invitees' and respondents' addresses masked, a write moderator and
// platform staff in full.
func TestTeamAndFormAnswerEmailsAreMaskedForObservers(t *testing.T) {
	actor, eventID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	forbidden := eventManagerModel.ErrEventManagementForbidden.Err()
	base := "/api/events/" + eventID.String() + "/manage/"
	routes := map[string]struct {
		url   string
		count int
	}{
		"teams":                    {base + "teams", 2},
		"teams table":              {base + "teams?page=1", 2},
		"team profile":             {base + "teams/" + uuid.Must(uuid.NewV7()).String(), 2},
		"form answers":             {base + "forms/" + uuid.Must(uuid.NewV7()).String() + "/answers", 1},
		"participant form answers": {base + "participant-form/answers", 1},
	}
	for name, route := range routes {
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
			t.Run(name+" "+tc.name, func(t *testing.T) {
				u := personalDataUC{accessContractUC: &accessContractUC{manageErr: tc.manageErr}, hideEmails: map[string]bool{}}
				w := httptest.NewRecorder()
				routerAs(u, actor, tc.role).ServeHTTP(w, httptest.NewRequest(http.MethodGet, route.url, nil))
				if w.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
				var body any
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatalf("response=%s error=%v", w.Body.String(), err)
				}
				got := emailsIn(body)
				if len(got) != route.count {
					t.Fatalf("emails=%v in %s", got, w.Body.String())
				}
				for _, email := range got {
					if email != tc.want {
						t.Fatalf("email=%q, want %q", email, tc.want)
					}
				}
			})
		}
	}
}

// An observer cannot find a person by an address it is not shown: the participants list and table and the teams
// table are asked to leave the addresses out of the search, the filters and the sorting.
func TestManageListsKeepEmailsOutOfQueriesForObservers(t *testing.T) {
	actor, eventID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	forbidden := eventManagerModel.ErrEventManagementForbidden.Err()
	base := "/api/events/" + eventID.String() + "/manage/"
	for name, url := range map[string]string{
		"participants":       base + "participants?search=olena",
		"participants table": base + "participants?page=1&search=olena",
		"teams table":        base + "teams?page=1",
	} {
		for _, tc := range []struct {
			name      string
			role      rbac.Role
			manageErr error
			hide      bool
		}{
			{name: "write moderator", role: rbac.RoleUser},
			{name: "observer", role: rbac.RoleUser, manageErr: forbidden, hide: true},
			{name: "admin viewer", role: rbac.RoleAdminViewer, manageErr: forbidden},
		} {
			t.Run(name+" "+tc.name, func(t *testing.T) {
				u := personalDataUC{accessContractUC: &accessContractUC{manageErr: tc.manageErr}, hideEmails: map[string]bool{}}
				w := httptest.NewRecorder()
				routerAs(u, actor, tc.role).ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
				if w.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
				hide, called := u.hideEmails[name]
				if !called || hide != tc.hide {
					t.Fatalf("HideEmails=%v (called %v), want %v", hide, called, tc.hide)
				}
			})
		}
	}
}
