package auth

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"

	"github.com/cybericebox/backend/internal/delivery/controller/http/response"
	draftKeyModel "github.com/cybericebox/backend/internal/model/draftKey"
	"github.com/cybericebox/backend/internal/model/rbac"
)

// draftKeyRequestsPerMinute bounds the draft-key requests of one person: the editor asks once per draft it opens.
const draftKeyRequestsPerMinute = 60

// draftKeyResponse is the draft key of this browser.
type draftKeyResponse struct {
	// ID names the key; the browser also holds it in the httpOnly cookie cib_draft_key.
	ID string
	// Key is the 256-bit key, standard base64 (44 characters).
	Key string
}

// draftKey godoc
// @Summary      Get or create the draft key of this browser
// @Description  The catalog editor seals its unsaved drafts in the browser (flags and secret values included) with this
// @Description  AES-256 key. One key per person and browser: the browser is named by the httpOnly, Secure, SameSite=Lax
// @Description  cookie cib_draft_key (Path=/api, Max-Age 2 days, refreshed on every call). The live key of the caller
// @Description  named by the cookie is returned; otherwise (no cookie, a stale key, the key of another person) a new key is
// @Description  made and the cookie set. A key unused for 2 days is deleted; signing out deletes the key of this
// @Description  browser, signing out the other sessions deletes the keys of the other browsers. Cache-Control: no-store.
// @Description  Only the catalog (exercises) and admin sites get a key: any other Origin (an event site included) is 403.
// @Tags         auth
// @Produce      json
// @Success      200  {object}  response.Response{data=draftKeyResponse}
// @Failure      401  {object}  response.Response
// @Failure      403  {object}  response.Response
// @Router       /me/draft-key [post]
func (h *Handler) draftKey(ctx *gin.Context) {
	claims, _ := rbac.CurrentUserSessionFromContext(ctx.Request.Context())
	ctx.Header("Cache-Control", "no-store")
	key, err := h.useCase.DraftKey(ctx.Request.Context(), claims.UserID, getDraftCookie(ctx))
	if err != nil {
		response.AbortWithError(ctx, err)
		return
	}
	setDraftCookie(ctx, key.ID, int(draftKeyModel.TTL.Seconds()))
	response.AbortWithData(ctx, draftKeyResponse{ID: key.ID, Key: base64.StdEncoding.EncodeToString(key.Key)})
}

// forgetDraftKey runs before the sign-out: the key of this browser is deleted and its cookie cleared. A failure is
// logged and never stops the sign-out; the key then goes when it is stale.
func (h *Handler) forgetDraftKey(ctx *gin.Context) {
	if claims, ok := rbac.CurrentUserSessionFromContext(ctx.Request.Context()); ok {
		if err := h.useCase.ForgetDraftKey(ctx.Request.Context(), claims.UserID, getDraftCookie(ctx)); err != nil {
			log.Warn().Err(err).Msg("Failed to delete the draft key of the browser at sign-out")
		}
	}
	setDraftCookie(ctx, "", -1)
	ctx.Next()
}

// forgetOtherDraftKeys deletes the draft keys of the browsers whose sessions were just signed out and keeps the key of
// this browser. The sessions are already revoked: a failure is logged, never answered (the keys go when they are stale).
// A single revoked session (DELETE /auth/sessions/:id) keeps its key: a key belongs to a browser, not to a session, so
// it cannot be told which key that session's browser holds.
func (h *Handler) forgetOtherDraftKeys(ctx *gin.Context, userID uuid.UUID) {
	if err := h.useCase.ForgetOtherDraftKeys(ctx.Request.Context(), userID, getDraftCookie(ctx)); err != nil {
		log.Warn().Err(err).Msg("Failed to delete the draft keys of the signed-out browsers")
	}
}

// requireEditorOrigin lets only the sites with the catalog editor (the catalog and the admin site) ask for a draft key:
// an event site, the main or the ID site is refused, even with the session cookie. The Origin decides; without it the
// Referer does; a request with neither is refused.
func (h *Handler) requireEditorOrigin(ctx *gin.Context) {
	source := ctx.GetHeader("Origin")
	if _, present := ctx.Request.Header["Origin"]; !present {
		source = ctx.GetHeader("Referer")
	}
	parsed, err := url.Parse(source)
	host := ""
	if err == nil && parsed.Scheme == "https" && parsed.User == nil {
		host = strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	}
	if host == "" || (host != h.hosts.Exercises && host != h.hosts.Admin) {
		log.Warn().Str("origin", ctx.GetHeader("Origin")).Str("referer", ctx.GetHeader("Referer")).
			Msg("Draft key refused: not the catalog or the admin site")
		response.AbortWithForbidden(ctx)
		return
	}
	ctx.Next()
}

func getDraftCookie(ctx *gin.Context) string {
	value, err := ctx.Cookie(draftKeyModel.CookieName)
	if err != nil {
		return ""
	}
	return value
}

func setDraftCookie(ctx *gin.Context, value string, maxAge int) {
	http.SetCookie(ctx.Writer, &http.Cookie{
		Name:     draftKeyModel.CookieName,
		Value:    value,
		Path:     draftKeyModel.CookiePath,
		MaxAge:   maxAge,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
