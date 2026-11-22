package assistant

import (
	"context"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/delivery/controller/http/response"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	"github.com/cybericebox/backend/internal/model/rbac"
	assistantUseCase "github.com/cybericebox/backend/internal/useCase/assistant"
	exerciseUseCase "github.com/cybericebox/backend/internal/useCase/exercise"
)

type (
	// IOpponentUseCase is the port of the opponent runs: the author tries an opponent of the variant's manifest against the
	// variant's test lab. Every call authorizes the actor like the test deploy of the same variant, and a run is found only
	// under the opponent and the variant it belongs to.
	IOpponentUseCase interface {
		StartOpponentRun(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID, index int) (assistantModel.Run, error)
		StartOpponentRuns(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID, indexes []int) ([]assistantModel.Run, error)
		OpponentRuns(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID, index int) ([]assistantModel.Run, error)
		OpponentRun(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID, index int, runID uuid.UUID) (assistantUseCase.RunView, error)
		StopOpponentRun(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID, index int, runID uuid.UUID) (assistantModel.Run, error)
		OpponentRunEvents(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID, index int, runID uuid.UUID) (func(ctx context.Context, after int64, limit int) ([]assistantModel.RunEvent, assistantModel.RunState, error), error)
	}

	// OpponentHandler is the API of the opponent runs of a variant, next to its measure runs.
	OpponentHandler struct {
		useCase IOpponentUseCase
		prot    IProtection
	}
)

func NewOpponent(useCase IOpponentUseCase, prot IProtection) *OpponentHandler {
	return &OpponentHandler{useCase: useCase, prot: prot}
}

func (h *OpponentHandler) Init(r *gin.RouterGroup) {
	g := r.Group("exercises/:id/versions/:versionID/variants/:variantID/opponents/:index/try")
	self := h.prot.RequirePermission(rbac.PermSelf)
	g.POST("", self, h.start)
	g.GET("", self, h.list)
	g.GET(":runID", self, h.get)
	g.GET(":runID/events", self, h.events)
	g.POST(":runID/stop", self, h.stop)
	// Several opponents of the variant together in one lab; each run is then read and stopped under its own opponent above.
	r.POST("exercises/:id/versions/:versionID/variants/:variantID/opponents/try", self, h.startMany)
}

// variantIDs reads the actor and the variant's path; false means the response is already written.
func variantIDs(c *gin.Context) (actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID, ok bool) {
	claims, found := rbac.CurrentUserSessionFromContext(c.Request.Context())
	if !found {
		response.AbortWithUnauthenticated(c)
		return
	}
	var err error
	if exerciseID, err = uuid.FromString(c.Param("id")); err == nil {
		if versionID, err = uuid.FromString(c.Param("versionID")); err == nil {
			variantID, err = uuid.FromString(c.Param("variantID"))
		}
	}
	if err != nil {
		response.AbortWithBadRequest(c)
		return
	}
	return exerciseUseCase.Actor{UserID: claims.UserID, Role: claims.Role}, exerciseID, versionID, variantID, true
}

// ids reads the actor, the variant's path and the opponent's position; false means the response is already written.
func (h *OpponentHandler) ids(c *gin.Context) (actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID, index int, ok bool) {
	if actor, exerciseID, versionID, variantID, ok = variantIDs(c); !ok {
		return
	}
	index, err := strconv.Atoi(c.Param("index"))
	if err != nil || index < 1 {
		response.AbortWithBadRequest(c)
		return actor, exerciseID, versionID, variantID, 0, false
	}
	return actor, exerciseID, versionID, variantID, index, true
}

// startManyRequest names the opponents to try together: their 1-based positions in the variant's manifest.
type startManyRequest struct {
	Indexes []int `json:"indexes"`
}

// startMany godoc
// @Summary  Try several opponents of a variant together against one test lab; each gets a run of its own
// @Tags     exercises
// @Accept   json
// @Produce  json
// @Param    body  body      startManyRequest  true  "the 1-based positions of the opponents"
// @Success  202   {object}  response.Response{data=[]runResponse}
// @Router   /exercises/{id}/versions/{versionID}/variants/{variantID}/opponents/try [post]
func (h *OpponentHandler) startMany(c *gin.Context) {
	actor, exerciseID, versionID, variantID, ok := variantIDs(c)
	if !ok {
		return
	}
	var body startManyRequest
	if err := c.ShouldBindJSON(&body); err != nil || !validIndexes(body.Indexes) {
		response.AbortWithBadRequest(c)
		return
	}
	runs, err := h.useCase.StartOpponentRuns(c.Request.Context(), actor, exerciseID, versionID, variantID, body.Indexes)
	if err != nil {
		response.AbortWithError(c, err)
		return
	}
	out := make([]runResponse, 0, len(runs))
	for _, r := range runs {
		out = append(out, runToResponse(r))
	}
	response.AbortWithDataStatus(c, http.StatusAccepted, out)
}

// validIndexes: one to MaxOpponents positions, each at least 1, none twice.
func validIndexes(indexes []int) bool {
	if len(indexes) == 0 || len(indexes) > exerciseModel.MaxOpponents {
		return false
	}
	seen := make(map[int]bool, len(indexes))
	for _, index := range indexes {
		if index < 1 || seen[index] {
			return false
		}
		seen[index] = true
	}
	return true
}

// start godoc
// @Summary  Try an opponent of a variant against its test lab
// @Tags     exercises
// @Produce  json
// @Success  202  {object}  response.Response{data=runResponse}
// @Router   /exercises/{id}/versions/{versionID}/variants/{variantID}/opponents/{index}/try [post]
func (h *OpponentHandler) start(c *gin.Context) {
	actor, exerciseID, versionID, variantID, index, ok := h.ids(c)
	if !ok {
		return
	}
	run, err := h.useCase.StartOpponentRun(c.Request.Context(), actor, exerciseID, versionID, variantID, index)
	if err != nil {
		response.AbortWithError(c, err)
		return
	}
	response.AbortWithDataStatus(c, http.StatusAccepted, runToResponse(run))
}

// list godoc
// @Summary  The runs of an opponent of a variant, newest first
// @Tags     exercises
// @Produce  json
// @Success  200  {object}  response.Response{data=[]runResponse}
// @Router   /exercises/{id}/versions/{versionID}/variants/{variantID}/opponents/{index}/try [get]
func (h *OpponentHandler) list(c *gin.Context) {
	actor, exerciseID, versionID, variantID, index, ok := h.ids(c)
	if !ok {
		return
	}
	runs, err := h.useCase.OpponentRuns(c.Request.Context(), actor, exerciseID, versionID, variantID, index)
	if err != nil {
		response.AbortWithError(c, err)
		return
	}
	out := make([]runResponse, 0, len(runs))
	for _, r := range runs {
		out = append(out, runToResponse(r))
	}
	response.AbortWithData(c, out)
}

// get godoc
// @Summary  One run of an opponent with its judged answers
// @Tags     exercises
// @Produce  json
// @Success  200  {object}  response.Response{data=runDetailResponse}
// @Router   /exercises/{id}/versions/{versionID}/variants/{variantID}/opponents/{index}/try/{runID} [get]
func (h *OpponentHandler) get(c *gin.Context) {
	actor, exerciseID, versionID, variantID, index, ok := h.ids(c)
	if !ok {
		return
	}
	id, ok := runID(c)
	if !ok {
		return
	}
	view, err := h.useCase.OpponentRun(c.Request.Context(), actor, exerciseID, versionID, variantID, index, id)
	if err != nil {
		response.AbortWithError(c, err)
		return
	}
	out := runDetailResponse{runResponse: runToResponse(view.Run), Answers: make([]answerResponse, 0, len(view.Answers))}
	for _, a := range view.Answers {
		out.Answers = append(out.Answers, answerResponse{Seq: a.Seq, SubtaskRef: a.SubtaskRef, Correct: a.Correct, At: a.At})
	}
	response.AbortWithData(c, out)
}

// stop godoc
// @Summary  Stop a run of an opponent; its lab is torn down once no other run plays in it
// @Tags     exercises
// @Produce  json
// @Success  200  {object}  response.Response{data=runResponse}
// @Router   /exercises/{id}/versions/{versionID}/variants/{variantID}/opponents/{index}/try/{runID}/stop [post]
func (h *OpponentHandler) stop(c *gin.Context) {
	actor, exerciseID, versionID, variantID, index, ok := h.ids(c)
	if !ok {
		return
	}
	id, ok := runID(c)
	if !ok {
		return
	}
	run, err := h.useCase.StopOpponentRun(c.Request.Context(), actor, exerciseID, versionID, variantID, index, id)
	if err != nil {
		response.AbortWithError(c, err)
		return
	}
	response.AbortWithData(c, runToResponse(run))
}

// events godoc
// @Summary  The events of a run of an opponent, from storage (resume with ?after= or Last-Event-ID)
// @Tags     exercises
// @Produce  text/event-stream
// @Router   /exercises/{id}/versions/{versionID}/variants/{variantID}/opponents/{index}/try/{runID}/events [get]
func (h *OpponentHandler) events(c *gin.Context) {
	actor, exerciseID, versionID, variantID, index, ok := h.ids(c)
	if !ok {
		return
	}
	id, ok := runID(c)
	if !ok {
		return
	}
	after, valid := lastEventID(c)
	if !valid {
		response.AbortWithBadRequest(c)
		return
	}
	read, err := h.useCase.OpponentRunEvents(c.Request.Context(), actor, exerciseID, versionID, variantID, index, id)
	if err != nil {
		response.AbortWithError(c, err)
		return
	}
	serveEvents(c, actor, after, read, func(ctx context.Context) error {
		_, authErr := h.useCase.OpponentRun(ctx, actor, exerciseID, versionID, variantID, index, id)
		return authErr
	})
}
