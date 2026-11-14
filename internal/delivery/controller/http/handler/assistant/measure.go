package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"

	"github.com/cybericebox/daemon/internal/delivery/controller/http/response"
	"github.com/cybericebox/daemon/internal/delivery/controller/http/sse"
	"github.com/cybericebox/daemon/internal/limits"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
	"github.com/cybericebox/daemon/internal/model/rbac"
	assistantUseCase "github.com/cybericebox/daemon/internal/useCase/assistant"
	exerciseUseCase "github.com/cybericebox/daemon/internal/useCase/exercise"
)

const (
	// eventsPage is how many stored events one read of the stream takes.
	eventsPage = 200
	// eventsPoll is how often the stream looks at the storage for new events.
	eventsPoll = 700 * time.Millisecond
)

type (
	// IMeasureUseCase is the port of the grader runs. Every call authorizes the actor like the test deploy of the same
	// variant: the author of the task, or a platform admin.
	IMeasureUseCase interface {
		StartGraderRun(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID) (assistantModel.Run, error)
		GraderRuns(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID) ([]assistantModel.Run, error)
		GraderRun(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID, runID uuid.UUID) (assistantUseCase.RunView, error)
		StopGraderRun(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID, runID uuid.UUID) (assistantModel.Run, error)
		GraderRunEvents(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID, runID uuid.UUID) (func(ctx context.Context, after int64, limit int) ([]assistantModel.RunEvent, assistantModel.RunState, error), error)
	}

	// MeasureHandler is the API of the grader runs of a variant, next to its test deploy: a run measures how hard the
	// task is by working it as a participant would.
	MeasureHandler struct {
		useCase IMeasureUseCase
		prot    IProtection
	}

	runResponse struct {
		ID string `json:"ID"`
		// State is starting, running, closing, finished, failed or stopped.
		State         string `json:"State"`
		FailureReason string `json:"FailureReason,omitempty"`
		VersionID     string `json:"VersionID"`
		VariantID     string `json:"VariantID"`
		// Opponent is the 1-based position of the opponent in the variant's manifest (an opponent run only), Role its role.
		Opponent int    `json:"Opponent,omitempty"`
		Role     string `json:"Role,omitempty"`
		// DeployID is the test lab of the run (it shows with the author's other test labs).
		DeployID string `json:"DeployID"`
		LastSeq  int64  `json:"LastSeq"`
		// Measurement is the brain's final aggregate, as it sent it.
		Measurement json.RawMessage `json:"Measurement,omitempty"`
		CreatedAt   time.Time       `json:"CreatedAt"`
		StartedAt   *time.Time      `json:"StartedAt,omitempty"`
		FinishedAt  *time.Time      `json:"FinishedAt,omitempty"`
		ExpiresAt   time.Time       `json:"ExpiresAt"`
	}

	answerResponse struct {
		Seq        int64     `json:"Seq"`
		SubtaskRef string    `json:"SubtaskRef"`
		Correct    bool      `json:"Correct"`
		At         time.Time `json:"At"`
	}

	runDetailResponse struct {
		runResponse
		Answers []answerResponse `json:"Answers"`
	}
)

func NewMeasure(useCase IMeasureUseCase, prot IProtection) *MeasureHandler {
	return &MeasureHandler{useCase: useCase, prot: prot}
}

func (h *MeasureHandler) Init(r *gin.RouterGroup) {
	// the same family and the same parameters as the variant's test deploy
	g := r.Group("exercises/:id/versions/:versionID/variants/:variantID/measure")
	self := h.prot.RequirePermission(rbac.PermSelf)
	g.POST("", self, h.start)
	g.GET("", self, h.list)
	g.GET(":runID", self, h.get)
	g.GET(":runID/events", self, h.events)
	g.POST(":runID/stop", self, h.stop)
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func runToResponse(run assistantModel.Run) runResponse {
	out := runResponse{
		ID: run.ID.String(), State: string(run.State), FailureReason: run.FailureReason, VersionID: run.VersionID.String(),
		VariantID: run.VariantID.String(), DeployID: run.DeployID.String(), LastSeq: run.LastSeq, Measurement: run.Measurement,
		CreatedAt: run.CreatedAt, StartedAt: timePtr(run.StartedAt), FinishedAt: timePtr(run.FinishedAt), ExpiresAt: run.ExpiresAt,
	}
	if run.Opponent > 0 {
		out.Opponent, out.Role = run.Opponent, run.Role
	}
	return out
}

// ids reads the actor and the variant's path; false means the response is already written.
func (h *MeasureHandler) ids(c *gin.Context) (actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID, ok bool) {
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

func runID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.FromString(c.Param("runID"))
	if err != nil {
		response.AbortWithBadRequest(c)
		return uuid.Nil, false
	}
	return id, true
}

// start godoc
// @Summary  Start a grader run on a variant
// @Tags     exercises
// @Produce  json
// @Success  202  {object}  response.Response{data=runResponse}
// @Router   /exercises/{id}/versions/{versionID}/variants/{variantID}/measure [post]
func (h *MeasureHandler) start(c *gin.Context) {
	actor, exerciseID, versionID, variantID, ok := h.ids(c)
	if !ok {
		return
	}
	run, err := h.useCase.StartGraderRun(c.Request.Context(), actor, exerciseID, versionID, variantID)
	if err != nil {
		response.AbortWithError(c, err)
		return
	}
	response.AbortWithDataStatus(c, http.StatusAccepted, runToResponse(run))
}

// list godoc
// @Summary  The grader runs of a variant, newest first
// @Tags     exercises
// @Produce  json
// @Success  200  {object}  response.Response{data=[]runResponse}
// @Router   /exercises/{id}/versions/{versionID}/variants/{variantID}/measure [get]
func (h *MeasureHandler) list(c *gin.Context) {
	actor, exerciseID, versionID, variantID, ok := h.ids(c)
	if !ok {
		return
	}
	runs, err := h.useCase.GraderRuns(c.Request.Context(), actor, exerciseID, versionID, variantID)
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
// @Summary  One grader run with its judged answers
// @Tags     exercises
// @Produce  json
// @Success  200  {object}  response.Response{data=runDetailResponse}
// @Router   /exercises/{id}/versions/{versionID}/variants/{variantID}/measure/{runID} [get]
func (h *MeasureHandler) get(c *gin.Context) {
	actor, exerciseID, versionID, variantID, ok := h.ids(c)
	if !ok {
		return
	}
	id, ok := runID(c)
	if !ok {
		return
	}
	view, err := h.useCase.GraderRun(c.Request.Context(), actor, exerciseID, versionID, variantID, id)
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
// @Summary  Stop a grader run; its lab is torn down
// @Tags     exercises
// @Produce  json
// @Success  200  {object}  response.Response{data=runResponse}
// @Router   /exercises/{id}/versions/{versionID}/variants/{variantID}/measure/{runID}/stop [post]
func (h *MeasureHandler) stop(c *gin.Context) {
	actor, exerciseID, versionID, variantID, ok := h.ids(c)
	if !ok {
		return
	}
	id, ok := runID(c)
	if !ok {
		return
	}
	run, err := h.useCase.StopGraderRun(c.Request.Context(), actor, exerciseID, versionID, variantID, id)
	if err != nil {
		response.AbortWithError(c, err)
		return
	}
	response.AbortWithData(c, runToResponse(run))
}

// lastEventID reads the position to resume from: ?after=, else the Last-Event-ID header an EventSource sends when it
// reconnects.
func lastEventID(c *gin.Context) (int64, bool) {
	raw := c.Query("after")
	if raw == "" {
		raw = c.GetHeader("Last-Event-ID")
	}
	if raw == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// writeEvent writes one stored frame as the brain sent it: the id is its sequence number, the data its text.
func writeEvent(c *gin.Context, e assistantModel.RunEvent) {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "id: %d\n", e.Seq)
	if e.Type != "" {
		_, _ = fmt.Fprintf(&b, "event: %s\n", strings.NewReplacer("\r", "", "\n", "").Replace(e.Type))
	}
	for _, line := range strings.Split(e.Data, "\n") {
		_, _ = fmt.Fprintf(&b, "data: %s\n", strings.TrimSuffix(line, "\r"))
	}
	b.WriteString("\n")
	_, _ = c.Writer.WriteString(b.String())
}

// events godoc
// @Summary  The events of a grader run, from storage (resume with ?after= or Last-Event-ID)
// @Tags     exercises
// @Produce  text/event-stream
// @Router   /exercises/{id}/versions/{versionID}/variants/{variantID}/measure/{runID}/events [get]
func (h *MeasureHandler) events(c *gin.Context) {
	actor, exerciseID, versionID, variantID, ok := h.ids(c)
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
	read, err := h.useCase.GraderRunEvents(c.Request.Context(), actor, exerciseID, versionID, variantID, id)
	if err != nil {
		response.AbortWithError(c, err)
		return
	}
	serveEvents(c, actor, after, read, func(ctx context.Context) error {
		_, authErr := h.useCase.GraderRun(ctx, actor, exerciseID, versionID, variantID, id)
		return authErr
	})
}

// serveEvents serves the stored events of a run to the page: from `after`, until the run is over, with a heartbeat. The actor is
// asked again from time to time (revalidate), since access may be taken away while the stream is open.
func serveEvents(c *gin.Context, actor exerciseUseCase.Actor, after int64, read func(ctx context.Context, after int64, limit int) ([]assistantModel.RunEvent, assistantModel.RunState, error), revalidateAccess func(ctx context.Context) error) {
	release, admitted := sse.Streams.Acquire("measure:"+actor.UserID.String(), limits.Get().StreamsPerUser)
	if !admitted {
		response.AbortWithTooManyRequests(c, response.StreamRetryAfter)
		return
	}
	defer release()
	flusher, streamCtx, cancel, err := sse.Open(c)
	if err != nil {
		response.AbortWithError(c, err)
		return
	}
	defer cancel()
	c.Header("X-Content-Type-Options", "nosniff")
	sse.WriteHeaders(c)
	poll := time.NewTicker(eventsPoll)
	defer poll.Stop()
	heartbeat := time.NewTicker(sse.HeartbeatInterval)
	defer heartbeat.Stop()
	revalidate := time.NewTicker(sse.RevalidateInterval)
	defer revalidate.Stop()
	for {
		// A run that is over is read in full and then the stream ends: the state is read before the events, so none is missed.
		events, state, readErr := read(streamCtx, after, eventsPage)
		if readErr != nil {
			return
		}
		for _, e := range events {
			writeEvent(c, e)
			after = e.Seq
		}
		if len(events) > 0 {
			flusher.Flush()
			continue
		}
		if !state.Active() {
			return
		}
		select {
		case <-streamCtx.Done():
			return
		case <-heartbeat.C:
			sse.Heartbeat(c.Writer, flusher)
		case <-revalidate.C:
			// The reader may lose access while the stream is open (a role taken away).
			if authErr := revalidateAccess(streamCtx); authErr != nil {
				return
			}
		case <-poll.C:
		}
	}
}
