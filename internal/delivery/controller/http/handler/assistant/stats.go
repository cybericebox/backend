package assistant

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/authz"
	"github.com/cybericebox/backend/internal/delivery/controller/http/response"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	"github.com/cybericebox/backend/internal/model/rbac"
	assistantUseCase "github.com/cybericebox/backend/internal/useCase/assistant"
)

type (
	// IStatsUseCase is the port of the statistics collection settings.
	IStatsUseCase interface {
		AssistantStatsDefaults(ctx context.Context) ([]assistantUseCase.SourceMode, error)
		AssistantSetStatsDefault(ctx context.Context, source string, mode assistantModel.StatsMode) error
		AssistantEventStats(ctx context.Context, event uuid.UUID) (assistantUseCase.EventStatsMode, error)
		AssistantSetEventStats(ctx context.Context, event uuid.UUID, mode assistantModel.StatsMode) error
	}

	// StatsHandler is the platform admin API of the statistics collection for cyber-brain: when the aggregates of a
	// source, or of one event, go to the brain. Platform admins only; the collection is off unless set.
	StatsHandler struct {
		useCase IStatsUseCase
		prot    IStatsProtection
	}

	// IStatsProtection is the protector of the statistics settings: the gate and its access declaration.
	IStatsProtection interface {
		IProtection
		Route(decl authz.Decl, legacy gin.HandlerFunc) gin.HandlerFunc
	}

	sourceModeResponse struct {
		Source string `json:"Source"`
		Mode   string `json:"Mode"`
	}

	statsDefaultsResponse struct {
		Sources []sourceModeResponse `json:"Sources"`
	}

	eventStatsResponse struct {
		// Mode is what the event is set to: default (follow the platform), off, final or continuous.
		Mode string `json:"Mode"`
		// Effective is the mode in force: Mode, or the platform default when Mode is default.
		Effective string `json:"Effective"`
	}

	statsModeRequest struct {
		Mode string `json:"Mode" binding:"required"`
	}
)

func NewStats(useCase IStatsUseCase, prot IStatsProtection) *StatsHandler {
	return &StatsHandler{useCase: useCase, prot: prot}
}

func (h *StatsHandler) Init(r *gin.RouterGroup) {
	g := r.Group("admin/assistant/statistics")
	read := h.prot.RequirePermission(rbac.PermPlatformSettingsRead)
	write := h.prot.RequirePermission(rbac.PermPlatformSettingsWrite)
	// Appendix A.3.3 #48-51 of the access spec: the statistics keys; per event also the event card reader and the
	// per-event source checkbox (§9 Q14).
	g.GET("", h.prot.Route(authz.AnyOf(authz.SourcePlatform, authz.CyberbrainStatsRead), read), h.defaults)
	g.PUT("defaults/:source", h.prot.Route(authz.AnyOf(authz.SourcePlatform, authz.CyberbrainStatsUpdate), write), h.setDefault)
	g.GET("events/:eventID", h.prot.Route(authz.AnyOf(authz.SourcePlatform, authz.CyberbrainStatsRead, authz.EventsRead), read), h.eventMode)
	g.PUT("events/:eventID", h.prot.Route(
		authz.AnyOf(authz.SourcePlatform, authz.CyberbrainStatsUpdate, authz.EventsStatsSourceUpdate), write,
	), h.setEventMode)
}

func (h *StatsHandler) defaults(c *gin.Context) {
	list, err := h.useCase.AssistantStatsDefaults(c.Request.Context())
	if err != nil {
		response.AbortWithError(c, err)
		return
	}
	out := statsDefaultsResponse{Sources: make([]sourceModeResponse, 0, len(list))}
	for _, s := range list {
		out.Sources = append(out.Sources, sourceModeResponse{Source: s.Source, Mode: string(s.Mode)})
	}
	response.AbortWithData(c, out)
}

func (h *StatsHandler) setDefault(c *gin.Context) {
	var in statsModeRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		response.AbortWithBadRequest(c, err)
		return
	}
	mode, ok := assistantModel.ParseStatsMode(in.Mode)
	if !ok {
		response.AbortWithError(c, assistantModel.ErrAssistantStatsInvalid.Err())
		return
	}
	if err := h.useCase.AssistantSetStatsDefault(c.Request.Context(), c.Param("source"), mode); err != nil {
		response.AbortWithError(c, err)
		return
	}
	response.AbortWithSuccess(c)
}

func (h *StatsHandler) eventID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.FromString(c.Param("eventID"))
	if err != nil {
		response.AbortWithBadRequest(c, err)
		return uuid.Nil, false
	}
	return id, true
}

func (h *StatsHandler) eventMode(c *gin.Context) {
	id, ok := h.eventID(c)
	if !ok {
		return
	}
	mode, err := h.useCase.AssistantEventStats(c.Request.Context(), id)
	if err != nil {
		response.AbortWithError(c, err)
		return
	}
	response.AbortWithData(c, eventStatsResponse{Mode: string(mode.Mode), Effective: string(mode.Effective)})
}

func (h *StatsHandler) setEventMode(c *gin.Context) {
	id, ok := h.eventID(c)
	if !ok {
		return
	}
	var in statsModeRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		response.AbortWithBadRequest(c, err)
		return
	}
	mode, ok := assistantModel.ParseStatsMode(in.Mode)
	if !ok {
		response.AbortWithError(c, assistantModel.ErrAssistantStatsInvalid.Err())
		return
	}
	if err := h.useCase.AssistantSetEventStats(c.Request.Context(), id, mode); err != nil {
		response.AbortWithError(c, err)
		return
	}
	response.AbortWithSuccess(c)
}
