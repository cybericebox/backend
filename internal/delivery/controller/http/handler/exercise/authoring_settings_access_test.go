package exercise_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/model/rbac"
)

// The editor's settings (lab capabilities and limits, the flag policy, the test-run lengths) are for who may author
// tasks somewhere, as the example archive and file uploads are: exercises.write, or an owner/manager of an event.
// Any other signed-in user, a read-only admin viewer included, is refused.
func TestAuthoringSettingsAreForAuthorsOnly(t *testing.T) {
	for _, route := range []string{"/api/exercises/capabilities", "/api/exercises/flag-policy", "/api/exercises/test-runs/lengths"} {
		for _, tc := range []struct {
			name    string
			role    rbac.Role
			manages bool
			want    int
		}{
			{name: "platform admin", role: rbac.RoleAdmin, want: http.StatusOK},
			{name: "event manager", role: rbac.RoleUser, manages: true, want: http.StatusOK},
			{name: "user who manages no event", role: rbac.RoleUser, want: http.StatusForbidden},
			{name: "admin viewer", role: rbac.RoleAdminViewer, want: http.StatusForbidden},
		} {
			t.Run(route+" "+tc.name, func(t *testing.T) {
				uc := &fakeUC{}
				if tc.manages {
					uc.managedEvent = uuid.Must(uuid.NewV7())
				}
				w := httptest.NewRecorder()
				engineAs(uc, tc.role).ServeHTTP(w, httptest.NewRequest(http.MethodGet, route, nil))
				if w.Code != tc.want {
					t.Fatalf("status %d, want %d: %s", w.Code, tc.want, w.Body.String())
				}
			})
		}
	}
}
