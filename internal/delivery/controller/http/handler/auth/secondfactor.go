package auth

import (
	"encoding/json"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cybericebox/backend/internal/delivery/controller/http/audit"
	"github.com/cybericebox/backend/internal/delivery/controller/http/response"
	"github.com/cybericebox/backend/internal/model/rbac"
	secondFactorModel "github.com/cybericebox/backend/internal/model/secondFactor"
	authUseCase "github.com/cybericebox/backend/internal/useCase/auth"
)

// The second factor of the account and the step-up (access-control design §6.6, routes N25-N29). Answers that carry a
// secret or recovery codes are never cached.

type secondFactorStatusResponse struct {
	TOTPEnabled       bool       `json:"TOTPEnabled"`
	TOTPConfirmedAt   *time.Time `json:"TOTPConfirmedAt"`
	Passkeys          int        `json:"Passkeys"`
	RecoveryCodesLeft int        `json:"RecoveryCodesLeft"`
	HasSecondFactor   bool       `json:"HasSecondFactor"`
	// StepUpMethod and StepUpExpiresAt describe the last step-up of this session; null without one.
	StepUpMethod    *string    `json:"StepUpMethod"`
	StepUpExpiresAt *time.Time `json:"StepUpExpiresAt"`
}

type totpBeginResponse struct {
	// Secret is the base32 secret for typing into an app; URI is the otpauth:// URI the QR code shows.
	Secret string `json:"Secret"`
	URI    string `json:"URI"`
}

type totpFinishRequest struct {
	Code string `json:"Code"`
}

type recoveryCodesResponse struct {
	// RecoveryCodes are shown this once; only their hashes are stored.
	RecoveryCodes []string `json:"RecoveryCodes"`
}

type stepUpRequest struct {
	// Method is passkey, totp or password (the password only for an account without a second factor).
	Method     string          `json:"Method"`
	Code       string          `json:"Code"`
	Password   string          `json:"Password"`
	CeremonyID string          `json:"CeremonyID"`
	Credential json.RawMessage `json:"Credential"`
}

type stepUpResponse struct {
	Method    string    `json:"Method"`
	At        time.Time `json:"At"`
	ExpiresAt time.Time `json:"ExpiresAt"`
}

type recoverySignInRequest struct {
	Email string `json:"Email"`
	// Password is required when the account has one: the code stands in for the second factor only.
	Password       string `json:"Password"`
	Code           string `json:"Code"`
	Redirect       string `json:"Redirect"`
	RecaptchaToken string `json:"RecaptchaToken"`
}

func noStore(ctx *gin.Context) { ctx.Header("Cache-Control", "no-store") }

// secondFactorStatus godoc
// @Summary  The second factor of the current user: authenticator app, passkeys, recovery codes left, step-up of this session
// @Tags     auth
// @Produce  json
// @Success  200  {object}  response.Response{data=secondFactorStatusResponse}
// @Failure  401  {object}  response.Response
// @Router   /auth/second-factor [get]
func (h *Handler) secondFactorStatus(ctx *gin.Context) {
	claims, _ := rbac.CurrentUserSessionFromContext(ctx.Request.Context())
	st, err := h.useCase.SecondFactorStatus(ctx.Request.Context(), claims.UserID)
	if err != nil {
		response.AbortWithError(ctx, err)
		return
	}
	out := secondFactorStatusResponse{
		TOTPEnabled: st.TOTPEnabled, TOTPConfirmedAt: st.TOTPConfirmedAt, Passkeys: st.Passkeys,
		RecoveryCodesLeft: st.RecoveryCodesLeft, HasSecondFactor: st.HasSecondFactor,
	}
	if st.StepUp != nil && st.StepUp.Fresh(time.Now(), secondFactorModel.StepUpWindow) {
		method, expires := string(st.StepUp.Method), st.StepUp.At.Add(secondFactorModel.StepUpWindow)
		out.StepUpMethod, out.StepUpExpiresAt = &method, &expires
	}
	response.AbortWithData(ctx, out)
}

// totpBegin godoc
// @Summary  Start adding an authenticator app: a new secret and its otpauth:// URI for the QR code (N25)
// @Tags     auth
// @Accept   json
// @Produce  json
// @Description  The owner is confirmed first: a step-up of this session in the last 5 minutes, or the current password (a recent sign-in for an account without one).
// @Param    body  body      reauthRequest  false  "current password"
// @Success  200  {object}  response.Response{data=totpBeginResponse}
// @Failure  400  {object}  response.Response
// @Failure  403  {object}  response.Response
// @Failure  409  {object}  response.Response
// @Router   /auth/totp/begin [post]
func (h *Handler) totpBegin(ctx *gin.Context) {
	claims, _ := rbac.CurrentUserSessionFromContext(ctx.Request.Context())
	req, ok := bindReauth(ctx)
	if !ok {
		return
	}
	enrolment, err := h.useCase.BeginTOTPEnrolment(ctx.Request.Context(), claims.UserID, req.CurrentPassword)
	if err != nil {
		response.AbortWithError(ctx, err)
		return
	}
	noStore(ctx)
	response.AbortWithData(ctx, totpBeginResponse{Secret: enrolment.Secret, URI: enrolment.URI})
}

// totpFinish godoc
// @Summary  Confirm the authenticator app with its first code; issues the 10 recovery codes, shown once (N26)
// @Tags     auth
// @Accept   json
// @Produce  json
// @Param    body  body      totpFinishRequest  true  "the first code of the app"
// @Success  200  {object}  response.Response{data=recoveryCodesResponse}
// @Failure  400  {object}  response.Response
// @Failure  429  {object}  response.Response
// @Router   /auth/totp/finish [post]
func (h *Handler) totpFinish(ctx *gin.Context) {
	claims, _ := rbac.CurrentUserSessionFromContext(ctx.Request.Context())
	var req totpFinishRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		response.AbortWithBadRequest(ctx, err)
		return
	}
	codes, err := h.useCase.FinishTOTPEnrolment(ctx.Request.Context(), claims.UserID, req.Code)
	if err != nil {
		response.AbortWithError(ctx, err)
		return
	}
	audit.SetTarget(ctx, "totp:"+claims.UserID.String())
	noStore(ctx)
	response.AbortWithData(ctx, recoveryCodesResponse{RecoveryCodes: codes})
}

// removeTOTP godoc
// @Summary  Remove the authenticator app (N27); needs a step-up with a second factor or a recovery code, refused for the last factor of a holder of a second-factor key
// @Tags     auth
// @Produce  json
// @Success  200  {object}  response.Response
// @Failure  400  {object}  response.Response
// @Failure  401  {object}  response.Response
// @Failure  404  {object}  response.Response
// @Router   /auth/totp [delete]
func (h *Handler) removeTOTP(ctx *gin.Context) {
	claims, _ := rbac.CurrentUserSessionFromContext(ctx.Request.Context())
	if err := h.useCase.RemoveTOTP(ctx.Request.Context(), claims.UserID); err != nil {
		response.AbortWithError(ctx, err)
		return
	}
	audit.SetTarget(ctx, "totp:"+claims.UserID.String())
	response.AbortWithSuccess(ctx)
}

// regenerateRecoveryCodes godoc
// @Summary  Replace the recovery codes with 10 new ones, shown once (N28); needs a second factor and a step-up made with it
// @Tags     auth
// @Produce  json
// @Success  200  {object}  response.Response{data=recoveryCodesResponse}
// @Failure  400  {object}  response.Response
// @Failure  401  {object}  response.Response
// @Router   /auth/recovery-codes [post]
func (h *Handler) regenerateRecoveryCodes(ctx *gin.Context) {
	claims, _ := rbac.CurrentUserSessionFromContext(ctx.Request.Context())
	codes, err := h.useCase.RegenerateRecoveryCodes(ctx.Request.Context(), claims.UserID)
	if err != nil {
		response.AbortWithError(ctx, err)
		return
	}
	noStore(ctx)
	response.AbortWithData(ctx, recoveryCodesResponse{RecoveryCodes: codes})
}

// stepUpPasskeyBegin godoc
// @Summary  Begin a passkey confirmation of the current user (user verification required)
// @Tags     auth
// @Produce  json
// @Success  200  {object}  response.Response{data=passkeyCeremonyResponse}
// @Failure  400  {object}  response.Response
// @Router   /auth/step-up/passkey/begin [post]
func (h *Handler) stepUpPasskeyBegin(ctx *gin.Context) {
	claims, _ := rbac.CurrentUserSessionFromContext(ctx.Request.Context())
	ceremony, err := h.useCase.BeginPasskeyStepUp(ctx.Request.Context(), claims.UserID)
	if err != nil {
		response.AbortWithError(ctx, err)
		return
	}
	response.AbortWithData(ctx, toPasskeyCeremonyResponse(ceremony))
}

// stepUp godoc
// @Summary  Confirm the current user on this session for 5 minutes (N29)
// @Tags     auth
// @Accept   json
// @Produce  json
// @Description  Method passkey (CeremonyID and Credential from /auth/step-up/passkey/begin), totp (Code), or password (Password; only for an account without a second factor, and an account without a password needs a recent sign-in). Google sign-in, email codes and SMS are not methods.
// @Param    body  body      stepUpRequest  true  "the confirmation"
// @Success  200  {object}  response.Response{data=stepUpResponse}
// @Failure  400  {object}  response.Response
// @Failure  403  {object}  response.Response
// @Failure  429  {object}  response.Response
// @Router   /auth/step-up [post]
func (h *Handler) stepUp(ctx *gin.Context) {
	claims, _ := rbac.CurrentUserSessionFromContext(ctx.Request.Context())
	var req stepUpRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		response.AbortWithBadRequest(ctx, err)
		return
	}
	s, err := h.useCase.StepUp(ctx.Request.Context(), claims.UserID, authUseCase.StepUpRequest{
		Method: secondFactorModel.Method(req.Method), Code: req.Code, Password: req.Password,
		CeremonyID: req.CeremonyID, Credential: req.Credential,
	})
	if err != nil {
		response.AbortWithError(ctx, err)
		return
	}
	response.AbortWithData(ctx, stepUpResponse{Method: string(s.Method), At: s.At, ExpiresAt: s.At.Add(secondFactorModel.StepUpWindow)})
}

// recoverySignIn godoc
// @Summary  Sign in with a one-time recovery code when the second factor is lost
// @Tags     auth
// @Accept   json
// @Produce  json
// @Description  The password is still needed when the account has one. The session may then enrol a new factor and get new codes; the use is journaled and mailed to the owner.
// @Param    body  body      recoverySignInRequest  true  "credentials and code"
// @Success  200  {object}  response.Response{data=object}
// @Failure  401  {object}  response.Response
// @Failure  429  {object}  response.Response
// @Router   /auth/sign-in/recovery [post]
func (h *Handler) recoverySignIn(ctx *gin.Context) {
	var req recoverySignInRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		response.AbortWithBadRequest(ctx, err)
		return
	}
	cookie, redirect, err := h.useCase.SignInWithRecoveryCode(
		ctx.Request.Context(), req.Email, req.Password, req.Code, req.Redirect, sessionMetadata(ctx),
	)
	if err != nil {
		response.AbortWithError(ctx, err)
		return
	}
	h.startSession(ctx, cookie)
	response.AbortWithData(ctx, gin.H{redirectURLKey: redirect})
}
