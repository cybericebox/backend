package eventself

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"

	"github.com/cybericebox/daemon/internal/delivery/controller/http/handler/labview"
	"github.com/cybericebox/daemon/internal/delivery/controller/http/response"
	infraModel "github.com/cybericebox/daemon/internal/model/infrastructure"
	"github.com/cybericebox/daemon/pkg/labaccess"
)

// labLinkOpener is the optional web-link capability of the use case. It is
// asserted at call time so the participant use case port and its fakes stay as
// they are.
type labLinkOpener interface {
	OpenOwnLabLink(ctx context.Context, eventID, userID, challengeID uuid.UUID, device string, port int32) (labaccess.Link, error)
}

type labLinkRequest struct {
	Device string `json:"Device" binding:"required"`
	Port   int32  `json:"Port"`
}

type labLinkResponse struct {
	LabID    uuid.UUID `json:"LabID"`
	Revision string    `json:"Revision"`
	// URL is https://<host>/_auth, without a query or a fragment; Grant is posted to it in the form field `grant`.
	URL       string    `json:"URL"`
	Grant     string    `json:"Grant"`
	ExpiresAt time.Time `json:"ExpiresAt"`
}

// openLabLink godoc
// @Summary Get the access grant that opens one web device of a task's lab
// @Description Returns URL (https://<device>-<code><letter>.<base>/_auth, no query) and Grant, a JWS signed for the caller's team lab group, lab, device and client; the page posts Grant to URL in a form (field grant, optional next). A grant is a bearer without replay memory: treat it like a password, never put it in an address. It lasts until the event ends (plus a buffer), capped by the laboratory's limit; the page fetches a new one on every click. The laboratory proxy turns it into its own session cookie; the platform sets no cookie. ExpiresAt is the end of the grant.
// @Tags events-self
// @Accept json
// @Produce json
// @Param id path string true "event ID"
// @Param challengeID path string true "event challenge ID"
// @Param body body labLinkRequest true "the device and port from the lab status"
// @Success 200 {object} response.Response{data=labLinkResponse}
// @Router /events/{id}/teams/challenges/{challengeID}/lab/link [post]
func (h *Handler) openLabLink(ctx *gin.Context) {
	eventID, ok := eventIDFromPath(ctx)
	if !ok {
		return
	}
	challengeID, err := uuid.FromString(ctx.Param("challengeID"))
	if err != nil {
		response.AbortWithBadRequest(ctx, err)
		return
	}
	claims, ok := claimsFromContext(ctx)
	if !ok {
		return
	}
	var req labLinkRequest
	if err = ctx.ShouldBindJSON(&req); err != nil {
		response.AbortWithBadRequest(ctx, err)
		return
	}
	opener, ok := h.useCase.(labLinkOpener)
	if !ok {
		response.AbortWithError(ctx, infraModel.ErrInfrastructureUnavailable.Err())
		return
	}
	link, err := opener.OpenOwnLabLink(ctx, eventID, claims.UserID, challengeID, req.Device, req.Port)
	if err != nil {
		response.AbortWithError(ctx, err)
		return
	}
	ctx.Header("Cache-Control", "private, no-store")
	response.AbortWithData(ctx, labLinkResponse{URL: link.URL, Grant: link.Grant, ExpiresAt: link.ExpiresAt, LabID: link.LabID, Revision: link.Revision})
}

// labLifecycle godoc
// @Summary Get the caller's canonical laboratory lifecycle
// @Tags events-self
// @Produce json
// @Param id path string true "event ID"
// @Param labID path string true "canonical lab ID"
// @Success 200 {object} response.Response{data=labLifecycleResponse}
// @Router /events/{id}/teams/labs/{labID} [get]
func (h *Handler) labLifecycle(ctx *gin.Context) {
	eventID, ok := eventIDFromPath(ctx)
	if !ok {
		return
	}
	labID, err := uuid.FromString(ctx.Param("labID"))
	if err != nil {
		response.AbortWithBadRequest(ctx, err)
		return
	}
	claims, ok := claimsFromContext(ctx)
	if !ok {
		return
	}
	lab, err := h.useCase.GetOwnLabLifecycle(ctx, eventID, claims.UserID, labID)
	if err != nil {
		response.AbortWithError(ctx, err)
		return
	}
	ctx.Header("Cache-Control", "private, no-store")
	response.AbortWithData(ctx, labLifecycleResponse{Lab: labview.ParticipantLab(&lab)})
}

type labLifecycleResponse struct {
	Lab *labview.ParticipantLabResponse `json:"Lab"`
}
