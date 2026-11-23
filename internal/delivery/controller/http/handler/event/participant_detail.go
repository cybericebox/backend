package event

import (
	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/delivery/controller/http/middleware"
	"github.com/cybericebox/backend/internal/delivery/controller/http/response"
	dispatchModel "github.com/cybericebox/backend/internal/model/notification/dispatch"
	eventUseCase "github.com/cybericebox/backend/internal/useCase/event"
)

// participantDetailResponse is the list row plus what the detail view shows.
type participantDetailResponse struct {
	participantResponse
	// TeamRole is 0 for the captain, 1 for a member; null without a team.
	TeamRole *int16 `json:"TeamRole"`
	// JoinedVia is open, approval or invitation.
	JoinedVia string `json:"JoinedVia"`
	Attempts  int64  `json:"Attempts"`
	Solves    int64  `json:"Solves"`
}

func toParticipantDetailResponse(v eventUseCase.ParticipantDetailView) participantDetailResponse {
	out := participantDetailResponse{
		participantResponse: toParticipantResponse(v.ParticipantView), JoinedVia: v.JoinedVia, Attempts: v.Attempts, Solves: v.Solves,
	}
	if v.TeamRole != nil {
		role := int16(*v.TeamRole)
		out.TeamRole = &role
	}
	return out
}

// getParticipantDetail godoc
// @Summary  One participant of the event for the manager
// @Tags     events
// @Produce  json
// @Param    id      path  string  true  "event ID"
// @Param    userID  path  string  true  "user ID"
// @Success  200  {object}  response.Response{data=participantDetailResponse}
// @Failure  404  {object}  response.Response
// @Router   /events/{id}/manage/participants/{userID} [get]
func (h *Handler) getParticipantDetail(ctx *gin.Context) {
	eventID, ok := parseEventID(ctx)
	if !ok {
		return
	}
	userID, err := uuid.FromString(ctx.Param("userID"))
	if err != nil {
		response.AbortWithBadRequest(ctx, err)
		return
	}
	v, err := h.useCase.GetParticipantDetail(ctx, eventID, userID)
	if err != nil {
		response.AbortWithError(ctx, err)
		return
	}
	out := toParticipantDetailResponse(v)
	private, ok := h.seesPersonalData(ctx, eventID)
	if !ok {
		return
	}
	if !private {
		out.Email = dispatchModel.MaskEmail(out.Email)
	}
	response.AbortWithData(ctx, out)
}

// seesPersonalData tells whether the caller sees the participants' addresses in full (owner decision 2026-10-19,
// as in mail and broadcasts): a read-only moderator gets them masked. ok false: the error is sent.
func (h *Handler) seesPersonalData(ctx *gin.Context, eventID uuid.UUID) (private, ok bool) {
	private, err := middleware.SeesEventPersonalData(ctx, eventID, h.useCase.RequireManageEvent)
	if err != nil {
		response.AbortWithError(ctx, err)
		return false, false
	}
	return private, true
}

// maskParticipantEmails masks the participants' addresses for a caller who does not see them in full.
func maskParticipantEmails(private bool, items []participantResponse) {
	if private {
		return
	}
	for i := range items {
		items[i].Email = dispatchModel.MaskEmail(items[i].Email)
	}
}

// maskTeamEmails masks the members' and invitees' addresses of the teams for a caller who does not see them in full.
func maskTeamEmails(private bool, teams []teamResponse) {
	if private {
		return
	}
	for i := range teams {
		for j := range teams[i].Members {
			teams[i].Members[j].Email = dispatchModel.MaskEmail(teams[i].Members[j].Email)
		}
		for j := range teams[i].PendingInvitations {
			teams[i].PendingInvitations[j].Email = dispatchModel.MaskEmail(teams[i].PendingInvitations[j].Email)
		}
	}
}
