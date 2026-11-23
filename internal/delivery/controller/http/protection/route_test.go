package protection_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/authz"
	"github.com/cybericebox/backend/internal/delivery/controller/http/protection"
	"github.com/cybericebox/backend/internal/delivery/controller/http/response"
	authModel "github.com/cybericebox/backend/internal/model/auth"
	"github.com/cybericebox/backend/internal/model/rbac"
	authUseCase "github.com/cybericebox/backend/internal/useCase/auth"
)

// Route records the declaration on the request and leaves the decision to the legacy gate (plan P1b): the same
// status as the bare gate for every role, and the declaration visible to what runs after it.
func TestRoute_RecordsTheDeclarationAndTheLegacyGateDecides(t *testing.T) {
	gin.SetMode(gin.TestMode)
	decl := authz.AnyOf(authz.SourcePlatform, authz.UsersList)

	run := func(role rbac.Role, wrapped bool) (int, authz.Decl, bool) {
		uc := &fakeUseCase{sessionResult: &authUseCase.SessionAuthResult{
			Claims: authModel.AuthClaims{SessionID: uuid.Must(uuid.NewV7()), UserID: uuid.Must(uuid.NewV7()), Role: role},
		}}
		p := newProt(uc)
		gate := p.RequirePermission(rbac.PermUsersRead)
		if wrapped {
			gate = p.Route(decl, gate)
		}
		var seen authz.Decl
		var found bool
		r := gin.New()
		r.Use(response.WithErrorHandler)
		r.GET("/x", gate, func(c *gin.Context) {
			seen, found = protection.DeclFromContext(c)
			c.Status(http.StatusOK)
		})
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.AddCookie(&http.Cookie{Name: authModel.SessionCookie, Value: "whatever"})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code, seen, found
	}

	for _, role := range []rbac.Role{rbac.RoleAdmin, rbac.RoleUser} {
		bare, _, _ := run(role, false)
		got, seen, found := run(role, true)
		if got != bare {
			t.Errorf("%s: Route answered %d, the legacy gate alone %d", role, got, bare)
		}
		if got == http.StatusOK && (!found || seen.String() != decl.String()) {
			t.Errorf("%s: declaration on the request = %v (found %v), want %s", role, seen, found, decl)
		}
	}
}

// A public route declares without a gate: nil legacy passes every request through.
func TestRoute_WithoutLegacyGatePassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := newProt(&fakeUseCase{})
	r := gin.New()
	r.Use(response.WithErrorHandler)
	r.GET("/open", p.Route(authz.PublicRoute(""), nil), func(c *gin.Context) {
		if d, ok := protection.DeclFromContext(c); !ok || d.Scope != authz.SourcePublic("") {
			t.Errorf("declaration on the request = %v (found %v)", d, ok)
		}
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/open", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("public route: got %d, want 200", w.Code)
	}
}
