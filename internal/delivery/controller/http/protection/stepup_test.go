package protection_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/config"
	"github.com/cybericebox/backend/internal/delivery/controller/http/protection"
	"github.com/cybericebox/backend/internal/delivery/controller/http/response"
	authModel "github.com/cybericebox/backend/internal/model/auth"
	"github.com/cybericebox/backend/internal/model/rbac"
	secondFactorModel "github.com/cybericebox/backend/internal/model/secondFactor"
	authUseCase "github.com/cybericebox/backend/internal/useCase/auth"
)

// stepUpUseCase is fakeUseCase with a recorded step-up.
type stepUpUseCase struct {
	*fakeUseCase
	stepUp secondFactorModel.StepUp
	asked  int
}

func (s *stepUpUseCase) StepUpState(context.Context, uuid.UUID) (secondFactorModel.StepUp, error) {
	s.asked++
	return s.stepUp, nil
}

func stepUpRouter(t *testing.T, required bool, s secondFactorModel.StepUp) (*httptest.ResponseRecorder, *stepUpUseCase) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	uc := &stepUpUseCase{fakeUseCase: &fakeUseCase{sessionResult: &authUseCase.SessionAuthResult{
		Claims: authModel.AuthClaims{SessionID: uuid.Must(uuid.NewV7()), UserID: uuid.Must(uuid.NewV7()), Role: rbac.RoleAdmin},
	}}, stepUp: s}
	p := protection.New(protection.Dependencies{UseCase: uc, Config: config.AuthConfig{
		Hosts: config.HostsConfig{Root: "example.test", API: "api.example.test", ID: "id.example.test"},
		Authz: config.AuthzConfig{StepUpRequired: required},
	}})
	r := gin.New()
	r.Use(response.WithErrorHandler)
	r.PATCH("/grant", p.RequirePermission(rbac.PermUsersRoleWrite), p.RequireStepUp(secondFactorModel.StepUpWindow),
		func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest(http.MethodPatch, "/grant", nil)
	req.AddCookie(&http.Cookie{Name: authModel.SessionCookie, Value: "whatever"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w, uc
}

func TestRequireStepUp_FlagOffAsksNothing(t *testing.T) {
	w, uc := stepUpRouter(t, false, secondFactorModel.StepUp{})
	if w.Code != http.StatusOK || uc.asked != 0 {
		t.Fatalf("flag off: want 200 without a lookup, got %d (asked %d)", w.Code, uc.asked)
	}
}

func TestRequireStepUp_FreshStepUpPasses(t *testing.T) {
	w, _ := stepUpRouter(t, true, secondFactorModel.StepUp{At: time.Now().Add(-4 * time.Minute), Method: secondFactorModel.MethodTOTP})
	if w.Code != http.StatusOK {
		t.Fatalf("a step-up of 4 minutes ago passes: got %d %s", w.Code, w.Body.String())
	}
}

func TestRequireStepUp_OldOrMissingStepUpIs401WithTheStepUpCode(t *testing.T) {
	for name, s := range map[string]secondFactorModel.StepUp{
		"none":  {},
		"older": {At: time.Now().Add(-6 * time.Minute), Method: secondFactorModel.MethodPasskey},
	} {
		w, _ := stepUpRouter(t, true, s)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: want 401, got %d", name, w.Code)
		}
		var body struct {
			Status struct{ Code int } `json:"Status"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v (%s)", name, err, w.Body.String())
		}
		if want := secondFactorModel.ErrStepUpRequired.Err().StatusCode().FullCode(); body.Status.Code != want {
			t.Fatalf("%s: want code %d, got %s", name, want, w.Body.String())
		}
		if w.Header().Get(protection.SignInURLHeader) != "" {
			t.Fatalf("%s: a step-up is no dead session: no sign-in URL", name)
		}
		for _, c := range w.Result().Cookies() {
			if c.Name == authModel.SessionCookie {
				t.Fatalf("%s: the session cookie is kept", name)
			}
		}
	}
}
