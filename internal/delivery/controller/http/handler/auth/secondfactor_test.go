package auth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"

	authHandler "github.com/cybericebox/backend/internal/delivery/controller/http/handler/auth"
	"github.com/cybericebox/backend/internal/delivery/controller/http/response"
	authModel "github.com/cybericebox/backend/internal/model/auth"
	secondFactorModel "github.com/cybericebox/backend/internal/model/secondFactor"
	authUseCase "github.com/cybericebox/backend/internal/useCase/auth"
)

// The second-factor methods of fakeUC answer nothing; factorUC records and answers for the tests below.
func (f *fakeUC) SecondFactorStatus(context.Context, uuid.UUID) (secondFactorModel.Status, error) {
	return secondFactorModel.Status{}, nil
}
func (f *fakeUC) BeginTOTPEnrolment(context.Context, uuid.UUID, string) (authUseCase.TOTPEnrolment, error) {
	return authUseCase.TOTPEnrolment{}, nil
}
func (f *fakeUC) FinishTOTPEnrolment(context.Context, uuid.UUID, string) ([]string, error) {
	return nil, nil
}
func (f *fakeUC) RemoveTOTP(context.Context, uuid.UUID) error { return nil }
func (f *fakeUC) RegenerateRecoveryCodes(context.Context, uuid.UUID) ([]string, error) {
	return nil, nil
}
func (f *fakeUC) BeginPasskeyStepUp(context.Context, uuid.UUID) (authUseCase.PasskeyCeremony, error) {
	return authUseCase.PasskeyCeremony{}, nil
}
func (f *fakeUC) StepUp(context.Context, uuid.UUID, authUseCase.StepUpRequest) (secondFactorModel.StepUp, error) {
	return secondFactorModel.StepUp{}, nil
}
func (f *fakeUC) SignInWithRecoveryCode(context.Context, string, string, string, string, authModel.SessionMetadata) (string, string, error) {
	return "", "", nil
}

type factorUC struct {
	*fakeUC
	user, totpCode, password string
	stepUp                   authUseCase.StepUpRequest
	codes                    []string
	err                      error
	status                   secondFactorModel.Status
}

func (f *factorUC) SecondFactorStatus(context.Context, uuid.UUID) (secondFactorModel.Status, error) {
	return f.status, f.err
}
func (f *factorUC) BeginTOTPEnrolment(_ context.Context, _ uuid.UUID, password string) (authUseCase.TOTPEnrolment, error) {
	f.password = password
	return authUseCase.TOTPEnrolment{Secret: "JBSWY3DPEHPK3PXP", URI: "otpauth://totp/x"}, f.err
}
func (f *factorUC) FinishTOTPEnrolment(_ context.Context, _ uuid.UUID, code string) ([]string, error) {
	f.totpCode = code
	return f.codes, f.err
}
func (f *factorUC) StepUp(_ context.Context, _ uuid.UUID, req authUseCase.StepUpRequest) (secondFactorModel.StepUp, error) {
	f.stepUp = req
	return secondFactorModel.StepUp{At: time.Now(), Method: req.Method}, f.err
}
func (f *factorUC) SignInWithRecoveryCode(_ context.Context, email, password, code, _ string, _ authModel.SessionMetadata) (string, string, error) {
	f.user, f.password, f.totpCode = email, password, code
	return "cookie", "https://id.example.test/profile/", f.err
}

func factorRouter(uc authHandler.IUseCase, signedIn bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(response.WithErrorHandler)
	if signedIn {
		r.Use(injectIdentity(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())))
	}
	api := r.Group("api")
	authHandler.NewAuthAPIHandler(uc, &fakeProt{}, testAuthConfig).Init(api, api)
	return r
}

func call(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestSecondFactorRoutesNeedASession(t *testing.T) {
	r := factorRouter(&factorUC{fakeUC: &fakeUC{}}, false)
	for _, rt := range [][2]string{
		{http.MethodGet, "/api/auth/second-factor"}, {http.MethodPost, "/api/auth/totp/begin"},
		{http.MethodPost, "/api/auth/totp/finish"}, {http.MethodDelete, "/api/auth/totp"},
		{http.MethodPost, "/api/auth/recovery-codes"}, {http.MethodPost, "/api/auth/step-up"},
		{http.MethodPost, "/api/auth/step-up/passkey/begin"},
	} {
		if w := call(r, rt[0], rt[1], "{}"); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session: want 401, got %d", rt[0], rt[1], w.Code)
		}
	}
}

func TestTOTPRoutesPassTheInputsAndAreNotCached(t *testing.T) {
	uc := &factorUC{fakeUC: &fakeUC{}, codes: []string{"AAAA-BBBB-CCCC"}}
	r := factorRouter(uc, true)

	w := call(r, http.MethodPost, "/api/auth/totp/begin", `{"CurrentPassword":"Secret!1"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "Secret!1", uc.password)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Contains(t, w.Body.String(), `"URI":"otpauth://totp/x"`)

	w = call(r, http.MethodPost, "/api/auth/totp/finish", `{"Code":"123456"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "123456", uc.totpCode)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Contains(t, w.Body.String(), `"RecoveryCodes":["AAAA-BBBB-CCCC"]`)
}

func TestStepUpRouteMapsTheBodyAndAnswersTheExpiry(t *testing.T) {
	uc := &factorUC{fakeUC: &fakeUC{}}
	r := factorRouter(uc, true)
	w := call(r, http.MethodPost, "/api/auth/step-up", `{"Method":"totp","Code":"654321"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, secondFactorModel.MethodTOTP, uc.stepUp.Method)
	require.Equal(t, "654321", uc.stepUp.Code)
	var body struct {
		Data struct {
			Method        string
			At, ExpiresAt time.Time
		} `json:"Data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	require.Equal(t, "totp", body.Data.Method)
	require.Equal(t, secondFactorModel.StepUpWindow, body.Data.ExpiresAt.Sub(body.Data.At))
}

func TestStepUpErrorsKeepTheirStatus(t *testing.T) {
	uc := &factorUC{fakeUC: &fakeUC{}, err: secondFactorModel.ErrStepUpFailed.Err()}
	w := call(factorRouter(uc, true), http.MethodPost, "/api/auth/step-up", `{"Method":"totp","Code":"1"}`)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

func TestRecoverySignInStartsTheSession(t *testing.T) {
	uc := &factorUC{fakeUC: &fakeUC{}}
	prot := &fakeProt{}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(response.WithErrorHandler)
	api := r.Group("api")
	authHandler.NewAuthAPIHandler(uc, prot, testAuthConfig).Init(api, api)

	w := call(r, http.MethodPost, "/api/auth/sign-in/recovery", `{"Email":"a@b.test","Password":"p","Code":"AAAA-BBBB-CCCC"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "cookie", prot.capturedSessionCookie)
	require.Equal(t, [3]string{"a@b.test", "p", "AAAA-BBBB-CCCC"}, [3]string{uc.user, uc.password, uc.totpCode})

	uc.err = secondFactorModel.ErrRecoverySignInFailed.Err()
	prot.capturedSessionCookie = ""
	w = call(r, http.MethodPost, "/api/auth/sign-in/recovery", `{"Email":"a@b.test","Code":"x"}`)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Empty(t, prot.capturedSessionCookie)
}

func TestSecondFactorStatusShowsOnlyAFreshStepUp(t *testing.T) {
	uc := &factorUC{fakeUC: &fakeUC{}, status: secondFactorModel.Status{
		TOTPEnabled: true, HasSecondFactor: true, RecoveryCodesLeft: 7,
		StepUp: &secondFactorModel.StepUp{At: time.Now().Add(-10 * time.Minute), Method: secondFactorModel.MethodTOTP},
	}}
	w := call(factorRouter(uc, true), http.MethodGet, "/api/auth/second-factor", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"RecoveryCodesLeft":7`)
	require.Contains(t, w.Body.String(), `"StepUpMethod":null`)
}
