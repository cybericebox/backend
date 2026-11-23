package auth

import (
	"context"
	"io"

	"github.com/cybericebox/backend/internal/authz"
	"github.com/cybericebox/backend/internal/config"

	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/delivery/controller/http/middleware"
	"github.com/cybericebox/backend/internal/limits"
	authModel "github.com/cybericebox/backend/internal/model/auth"
	draftKeyModel "github.com/cybericebox/backend/internal/model/draftKey"
	passkeyModel "github.com/cybericebox/backend/internal/model/passkey"
	"github.com/cybericebox/backend/internal/model/rbac"
	tablePrefsModel "github.com/cybericebox/backend/internal/model/tablePrefs"
	userModel "github.com/cybericebox/backend/internal/model/user"
	authUseCase "github.com/cybericebox/backend/internal/useCase/auth"
)

type (
	Handler struct {
		useCase IUseCase
		prot    IProtection
		hosts   config.HostsConfig
		// oauthCookieMaxAge is the life of the short OAuth state cookies, seconds (OAUTH_STATE_TTL).
		oauthCookieMaxAge int
	}

	IUseCase interface {
		SignIn(
			ctx context.Context,
			email, password, redirect string,
			meta authModel.SessionMetadata,
		) (string, string, error)
		IsTrustedRedirect(rawURL string) bool
		ListSessions(
			ctx context.Context,
			userID, currentSessionID uuid.UUID,
		) ([]authUseCase.SessionInfo, error)
		RevokeSession(ctx context.Context, userID, sessionID uuid.UUID) error
		RevokeOtherSessions(ctx context.Context, userID, currentSessionID uuid.UUID) error
		BeginEmailRegistration(ctx context.Context, email, redirect string) error
		GetSetupContext(ctx context.Context, setupToken string) (*authUseCase.SetupContext, error)
		CompleteRegistration(
			ctx context.Context,
			setupToken, firstName, lastName, password string,
			acceptedDocuments []string,
			redirect string,
			meta authModel.SessionMetadata,
		) (string, string, error)
		PasswordPolicy() authUseCase.PasswordPolicy
		ForgotPassword(ctx context.Context, email string) error
		ResetPassword(ctx context.Context, code, password string) error
		SetAccountPassword(
			ctx context.Context,
			userID uuid.UUID,
			oldPassword, newPassword string,
		) error
		GetGoogleLoginURL(redirect, loginHint string) (string, string, error)
		GetGoogleSetupURL(ctx context.Context, setupToken, redirect string) (string, string, error)
		StartGoogleLink(
			ctx context.Context,
			userID uuid.UUID,
			currentPassword string,
		) (string, string, error)
		GoogleAuth(
			ctx context.Context,
			code, state string,
			meta authModel.SessionMetadata,
		) (string, string, error)
		BeginGoogleRegistration(
			ctx context.Context,
			code, state string,
			meta authModel.SessionMetadata,
		) (authUseCase.GoogleRegistrationResult, error)
		LinkGoogleToSetupFromOAuth(
			ctx context.Context,
			setupToken, code, state string,
		) (string, error)
		SessionEmail(ctx context.Context, sessionCookie string) (string, error)
		GoogleLinkPendingOf(ctx context.Context, ticket string) (authUseCase.GoogleLinkPending, error)
		SendGoogleLinkCode(ctx context.Context, ticket string) (authUseCase.GoogleLinkCodeSent, error)
		ConfirmGoogleLink(ctx context.Context, ticket, password, code string, meta authModel.SessionMetadata) (string, string, error)
		CancelGoogleLink(ctx context.Context, ticket string) error

		// slice 4a — account self-management
		GetAccount(ctx context.Context, userID uuid.UUID) (*authUseCase.AccountInfo, error)
		GetSelfProfile(ctx context.Context, userID uuid.UUID) (*authUseCase.UserInfo, error)
		GetTablePrefs(ctx context.Context, userID uuid.UUID, key string) (*tablePrefsModel.Prefs, error)
		SaveTablePrefs(ctx context.Context, userID uuid.UUID, key string, prefs tablePrefsModel.Prefs) (tablePrefsModel.Prefs, error)
		ResetTablePrefs(ctx context.Context, userID uuid.UUID, key string) error
		DraftKey(ctx context.Context, userID uuid.UUID, cookieID string) (draftKeyModel.Key, error)
		ForgetDraftKey(ctx context.Context, userID uuid.UUID, cookieID string) error
		ForgetOtherDraftKeys(ctx context.Context, userID uuid.UUID, cookieID string) error
		ExportMyData(ctx context.Context, userID uuid.UUID) (*userModel.DataExport, error)
		UpdateAccountProfile(
			ctx context.Context,
			userID uuid.UUID,
			firstName, lastName string,
		) error
		RequestEmailChange(
			ctx context.Context,
			userID uuid.UUID,
			newEmail, currentPassword string,
		) error
		ConfirmEmailChange(ctx context.Context, code string) error
		UnlinkGoogle(ctx context.Context, userID uuid.UUID, currentPassword string) error
		DeleteAccount(ctx context.Context, userID uuid.UUID, currentPassword string) error
		LinkGoogleToAccountFromOAuth(
			ctx context.Context,
			sessionCookieValue, code, state string,
		) error

		// passkeys (WebAuthn)
		BeginPasskeySignIn(ctx context.Context) (authUseCase.PasskeyCeremony, error)
		PasskeySignIn(
			ctx context.Context,
			ceremonyID, redirect string,
			assertion []byte,
			meta authModel.SessionMetadata,
		) (string, string, error)
		BeginPasskeyRegistration(
			ctx context.Context,
			userID uuid.UUID,
			currentPassword string,
		) (authUseCase.PasskeyCeremony, error)
		FinishPasskeyRegistration(
			ctx context.Context,
			userID uuid.UUID,
			ceremonyID, name string,
			credential []byte,
		) (passkeyModel.Passkey, error)
		ListPasskeys(ctx context.Context, userID uuid.UUID) ([]passkeyModel.Passkey, error)
		RemovePasskey(ctx context.Context, userID, passkeyID uuid.UUID, currentPassword string) error

		// avatars (S3-backed)
		UploadAvatar(
			ctx context.Context,
			userID uuid.UUID,
			r io.Reader,
			size int64,
			contentType string,
		) error
		RemoveAvatar(ctx context.Context, userID uuid.UUID) error
		GetAvatar(ctx context.Context, userID uuid.UUID) (io.ReadCloser, string, error)
	}

	// IProtection is the subset of the protection middleware the handler calls.
	IProtection interface {
		Authenticate(ctx *gin.Context, value string)
		DeAuthenticate(ctx *gin.Context)
		RequireCaptcha(action string) gin.HandlerFunc
		RequirePermission(required rbac.Permission) gin.HandlerFunc
		Route(decl authz.Decl, legacy gin.HandlerFunc) gin.HandlerFunc
	}
)

func NewAuthAPIHandler(useCase IUseCase, prot IProtection, cfg config.AuthConfig) *Handler {
	return &Handler{
		useCase:           useCase,
		prot:              prot,
		hosts:             cfg.Hosts,
		oauthCookieMaxAge: int(cfg.OAuth.StateTTL.Seconds()),
	}
}

// The public auth routes carry no request-rate guard: a flood is bounded by the use case's per-account
// failure lockouts and per-address mail quotas, and a whole computer lab shares one client address, which
// no limit is keyed on. The signed-in password and email checks are limited per user.

func (h *Handler) Init(public, secured *gin.RouterGroup) {
	lim := limits.Get()
	selfCheck := middleware.RateLimitPerUser(lim.AccountActions, lim.AccountActionsWindow)

	// Access declarations (Appendix A.3.2 of the access spec): the public flows read no visibility setting, the
	// signed-in routes act on the caller's own account only.
	open := h.prot.Route(authz.PublicRoute(""), nil)

	pub := public.Group("auth")
	pub.POST("sign-in", open, h.prot.RequireCaptcha("signIn"), h.signIn)
	pub.POST("sign-up", open, h.prot.RequireCaptcha("signUp"), h.signUp)
	pub.GET("last-email", open, h.getLastEmail)
	pub.DELETE("last-email", open, h.forgetLastEmail)
	pub.GET("setup", open, h.getSetup)
	pub.POST("setup", open, h.completeSetup)

	// Passkey sign-in: the ceremony start is behind the captcha like the other sign-in forms; the finish is
	// bound to a one-time server challenge and verified by signature.
	passkeys := pub.Group("passkeys/sign-in")
	passkeys.POST("begin", open, h.prot.RequireCaptcha("passkeySignIn"), h.passkeySignInBegin)
	passkeys.POST("finish", open, h.passkeySignInFinish)

	password := pub.Group("password")
	password.GET("policy", open, h.passwordPolicy)
	password.POST("reset-request", open, h.prot.RequireCaptcha("forgotPassword"), h.requestPasswordReset)
	password.POST("reset", open, h.resetPassword)
	password.POST("change", h.prot.Route(authz.OwnData(), h.prot.RequirePermission(rbac.PermSelf)), selfCheck, h.changePassword)

	google := pub.Group("google")
	google.GET("", open, h.googleRedirect)
	google.GET("register", open, h.googleRegisterRedirect)
	google.GET("setup", open, h.googleSetupRedirect)
	google.GET("callback", open, h.googleCallback)
	// The pending link of a Google sign-in whose owner is not confirmed yet (the ticket is in a cookie).
	pending := google.Group("pending")
	pending.GET("", open, h.googleLinkPending)
	pending.POST("code", open, h.prot.RequireCaptcha("googleLinkCode"), h.googleLinkSendCode)
	pending.POST("confirm", open, h.googleLinkConfirm)
	pending.DELETE("", open, h.googleLinkCancel)

	sec := secured.Group("auth")
	self := h.prot.RequirePermission(rbac.PermSelf)
	own := h.prot.Route(authz.OwnData(), self)
	sec.POST("sign-out", own, h.forgetDraftKey, h.prot.DeAuthenticate)
	sec.GET("sessions", own, h.listSessions)
	sec.DELETE("sessions/:id", own, h.revokeSession)
	sec.DELETE("sessions", own, h.revokeOtherSessions)

	// slice 4a — account self-management
	sec.GET("account", own, h.getAccount)
	sec.GET("me", own, h.getSelfProfile)
	sec.GET("account/export", own, h.exportMyData)
	sec.GET("account/table-prefs/:tableKey", own, h.getTablePrefs)
	sec.PUT("account/table-prefs/:tableKey", own, h.saveTablePrefs)
	sec.DELETE("account/table-prefs/:tableKey", own, h.resetTablePrefs)
	sec.PATCH("account/profile", own, h.updateProfile)
	sec.POST(
		"account/email",
		own,
		selfCheck,
		h.prot.RequireCaptcha("emailChange"),
		h.requestEmailChange,
	)
	sec.DELETE("account", own, h.deleteAccount)
	sec.POST("account/avatar", own, h.uploadAvatar)
	sec.DELETE("account/avatar", own, h.removeAvatar)
	sec.GET("passkeys", own, h.listPasskeys)
	sec.POST("passkeys/registration/begin", own, selfCheck, h.passkeyRegistrationBegin)
	sec.POST("passkeys/registration/finish", own, h.passkeyRegistrationFinish)
	sec.DELETE("passkeys/:id", own, h.removePasskey)
	sec.POST("google/link", own, h.googleLink)
	sec.DELETE("google/link", own, h.unlinkGoogle)

	// The key the catalog editor seals its local drafts with: one per person and browser.
	secured.Group("me").POST("draft-key", self, h.requireEditorOrigin, h.draftKey)

	pub.POST("account/email/confirm", open, h.confirmEmailChange)
	// Public avatar proxy: streams the stored image so the bucket stays private.
	pub.GET("avatar/:id", open, middleware.PublicMedia, h.getAvatar)
}
