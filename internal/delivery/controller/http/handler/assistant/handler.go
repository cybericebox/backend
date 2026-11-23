// Package assistant is the HTTP delivery of the authoring assistant: the routes the exercises site calls under
// /api/assistant, passed on to cyber-brain. The platform has already authenticated the person; the brain only gets an
// opaque subject. The handler does not interpret what the chat says: request bodies, the event stream and the answers
// go through as they are. The one route with platform logic is save (import the finished task, then tell the brain).
package assistant

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"

	"github.com/cybericebox/backend/internal/authz"
	"github.com/cybericebox/backend/internal/delivery/controller/http/download"
	"github.com/cybericebox/backend/internal/delivery/controller/http/middleware"
	"github.com/cybericebox/backend/internal/delivery/controller/http/response"
	"github.com/cybericebox/backend/internal/delivery/controller/http/sse"
	"github.com/cybericebox/backend/internal/limits"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	"github.com/cybericebox/backend/internal/model/rbac"
	assistantUseCase "github.com/cybericebox/backend/internal/useCase/assistant"
	exerciseUseCase "github.com/cybericebox/backend/internal/useCase/exercise"
)

const (
	// maxJSONBody caps a chat request body (a message, a session seed).
	maxJSONBody = 1 << 20
	// maxAttachmentBody caps an attachment upload; the brain applies its own, stricter limit.
	maxAttachmentBody = 50 << 20
	// maxAnswerBody caps a pass-through JSON answer.
	maxAnswerBody = 4 << 20
	// maxFrameBytes caps one line of the event stream.
	maxFrameBytes = 1 << 20
	// requestedWith is the non-empty marker header of an upload: the brain's CSRF guard asks for it.
	requestedWith = "cybericebox-daemon"
)

// sessionIDPattern is what a brain session or attachment id may look like in a path: it keeps a crafted id from
// reaching another brain route.
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]{0,127}$`)

type (
	IProtection interface {
		RequirePermission(required rbac.Permission) gin.HandlerFunc
		Route(decl authz.Decl, legacy gin.HandlerFunc) gin.HandlerFunc
	}

	IUseCase interface {
		// RequireAuthor: whoever may author tasks somewhere (an admin, or a manager of an event) may use the assistant.
		RequireAuthor(ctx context.Context, actor exerciseUseCase.Actor) error
		AssistantProxy(ctx context.Context, user uuid.UUID, call assistantUseCase.Call) (*http.Response, error)
		AssistantSaveSession(ctx context.Context, actor exerciseUseCase.Actor, sessionID string) (done bool, err error)
	}

	Handler struct {
		useCase IUseCase
		prot    IProtection
	}
)

func New(useCase IUseCase, prot IProtection) *Handler { return &Handler{useCase: useCase, prot: prot} }

func (h *Handler) Init(r *gin.RouterGroup) {
	g := r.Group("assistant")
	// self: a signed-in person; author: one who may author tasks somewhere (the same rule as importing a task)
	self := h.prot.RequirePermission(rbac.PermSelf)
	long := download.LongOperation()
	g.GET("ideas", h.prot.Route(authz.AnyOf(authz.SourcePlatform, authz.CyberbrainChatUse), self), h.author, h.passJSON(http.MethodGet, "/v1/assistant/ideas"))
	g.POST("sessions", h.prot.Route(authz.AnyOf(authz.SourcePlatform, authz.CyberbrainChatUse), self), h.author, h.passJSON(http.MethodPost, "/v1/assistant/sessions"))
	g.POST("sessions/:id/messages", h.prot.Route(authz.AnyOf(authz.SourcePlatform, authz.CyberbrainChatUse), self), h.author, h.passSessionJSON(http.MethodPost, "/messages"))
	g.POST("sessions/:id/attachments", h.prot.Route(authz.AnyOf(authz.SourcePlatform, authz.CyberbrainChatUse), self), h.author, h.uploadAttachment)
	g.DELETE("sessions/:id/attachments/:aid", h.prot.Route(authz.AnyOf(authz.SourcePlatform, authz.CyberbrainChatUse), self), h.author, h.deleteAttachment)
	g.POST("sessions/:id/start", h.prot.Route(authz.AnyOf(authz.SourcePlatform, authz.CyberbrainChatUse), self), h.author, h.passSessionJSON(http.MethodPost, "/start"))
	g.POST("sessions/:id/stop", h.prot.Route(authz.AnyOf(authz.SourcePlatform, authz.CyberbrainChatUse), self), h.author, h.passSessionJSON(http.MethodPost, "/stop"))
	// save imports the finished task (a background import waits for its images), then tells the brain
	g.POST("sessions/:id/save", long, h.prot.Route(authz.AnyOf(authz.SourcePlatform, authz.CyberbrainChatUse).WithData(authz.ExercisesCreate, authz.ExercisesUpdate, authz.EventExercisesCreate, authz.EventExercisesUpdate), self), h.author, h.save)
	g.GET("sessions/:id/archive", h.prot.Route(authz.AnyOf(authz.SourcePlatform, authz.CyberbrainSessionsRead), self), h.author, h.archive)
	g.DELETE("sessions/:id", h.prot.Route(authz.AnyOf(authz.SourcePlatform, authz.CyberbrainChatUse), self), h.author, h.passSessionJSON(http.MethodDelete, ""))
	g.GET("sessions/:id/events", h.prot.Route(authz.AnyOf(authz.SourcePlatform, authz.CyberbrainSessionsRead), self), h.author, h.events)
}

const actorKey = "assistant.actor"

// author lets through whoever may author tasks somewhere (an admin, or a manager of an event) and keeps them as the
// actor of the request; it answers 401/403 itself otherwise.
func (h *Handler) author(c *gin.Context) {
	claims, ok := rbac.CurrentUserSessionFromContext(c.Request.Context())
	if !ok {
		response.AbortWithUnauthenticated(c)
		return
	}
	actor := exerciseUseCase.Actor{UserID: claims.UserID, Role: claims.Role}
	if err := h.useCase.RequireAuthor(c.Request.Context(), actor); err != nil {
		response.AbortWithError(c, err)
		return
	}
	c.Set(actorKey, actor)
	c.Next()
}

// actor is the author that passed the author gate.
func (h *Handler) actor(c *gin.Context) (exerciseUseCase.Actor, bool) {
	value, _ := c.Get(actorKey)
	actor, ok := value.(exerciseUseCase.Actor)
	if !ok {
		response.AbortWithUnauthenticated(c)
		return exerciseUseCase.Actor{}, false
	}
	return actor, true
}

// sessionPath is the brain path of a session route; false answers 404 for an id that cannot be a session id.
func sessionPath(c *gin.Context, tail string) (string, bool) {
	id := c.Param("id")
	if !sessionIDPattern.MatchString(id) {
		response.AbortWithNotFound(c)
		return "", false
	}
	return "/v1/assistant/sessions/" + id + tail, true
}

// passJSON passes one route with a JSON body and answer straight through.
func (h *Handler) passJSON(method, path string) gin.HandlerFunc {
	return func(c *gin.Context) { h.relayJSON(c, method, path) }
}

func (h *Handler) passSessionJSON(method, tail string) gin.HandlerFunc {
	return func(c *gin.Context) {
		path, ok := sessionPath(c, tail)
		if !ok {
			return
		}
		h.relayJSON(c, method, path)
	}
}

// relayJSON sends the caller's JSON body (an empty one is "{}") and relays the answer as it is.
func (h *Handler) relayJSON(c *gin.Context, method, path string) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	call := assistantUseCase.Call{Method: method, Path: path, Header: http.Header{"Accept": {"application/json"}}}
	if method == http.MethodPost {
		middleware.LimitBody(c, maxJSONBody)
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			response.AbortWithBadRequest(c, err)
			return
		}
		if len(bytes.TrimSpace(body)) == 0 {
			body = []byte("{}")
		}
		call.Header.Set("Content-Type", "application/json")
		call.Body, call.ContentLength = bytes.NewReader(body), int64(len(body))
	}
	resp, err := h.useCase.AssistantProxy(c.Request.Context(), actor.UserID, call)
	if err != nil {
		response.AbortWithError(c, err)
		return
	}
	relayAnswer(c, resp)
}

// relayAnswer sends a JSON answer of the brain in the platform's own envelope ({Status, Data}), built by the shared
// response helpers, so the page unwraps it like any other answer. An empty answer is the same as our other empty
// successes. An answer of another type, or one that is not JSON, is not relayed: it is an upstream failure.
func relayAnswer(c *gin.Context, resp *http.Response) {
	defer func() { _ = resp.Body.Close() }()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBody))
	if err != nil {
		response.AbortWithError(c, err)
		return
	}
	privateNoStore(c)
	c.Header("X-Content-Type-Options", "nosniff")
	if len(bytes.TrimSpace(answer)) == 0 {
		if resp.StatusCode == http.StatusNoContent {
			response.AbortWithNoContent(c)
			return
		}
		response.AbortWithSuccess(c)
		return
	}
	if mediaType(resp.Header.Get("Content-Type")) != "application/json" || !json.Valid(answer) {
		response.AbortWithError(c, wrongType(resp))
		return
	}
	response.AbortWithDataStatus(c, resp.StatusCode, json.RawMessage(answer))
}

// uploadAttachment streams the multipart body to the brain unparsed, with the marker header its CSRF guard asks for.
func (h *Handler) uploadAttachment(c *gin.Context) {
	path, ok := sessionPath(c, "/attachments")
	if !ok {
		return
	}
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	middleware.LimitBody(c, maxAttachmentBody)
	resp, err := h.useCase.AssistantProxy(c.Request.Context(), actor.UserID, assistantUseCase.Call{
		Method: http.MethodPost, Path: path,
		Header: http.Header{"Content-Type": {c.GetHeader("Content-Type")}, "X-Requested-With": {requestedWith}, "Accept": {"application/json"}},
		Body:   c.Request.Body, ContentLength: c.Request.ContentLength,
	})
	if err != nil {
		response.AbortWithError(c, err)
		return
	}
	relayAnswer(c, resp)
}

func (h *Handler) deleteAttachment(c *gin.Context) {
	aid := c.Param("aid")
	if !sessionIDPattern.MatchString(aid) {
		response.AbortWithNotFound(c)
		return
	}
	path, ok := sessionPath(c, "/attachments/"+aid)
	if !ok {
		return
	}
	h.relayJSON(c, http.MethodDelete, path)
}

// save imports the finished task as the caller and tells the brain: 200 when it did, 202 when the import of a task with
// device images was queued (the brain is told when it is done).
func (h *Handler) save(c *gin.Context) {
	id := c.Param("id")
	if !sessionIDPattern.MatchString(id) {
		response.AbortWithNotFound(c)
		return
	}
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	done, err := h.useCase.AssistantSaveSession(c.Request.Context(), actor, id)
	if err != nil {
		response.AbortWithError(c, err)
		return
	}
	if !done {
		// The task archive names device images: its import runs in the background and the brain's `saved` event on the
		// session stream says it is done.
		response.AbortWithDataStatus(c, http.StatusAccepted, struct{}{})
		return
	}
	response.AbortWithSuccess(c)
}

// archive streams the finished task archive with the brain's file name.
func (h *Handler) archive(c *gin.Context) {
	path, ok := sessionPath(c, "/archive")
	if !ok {
		return
	}
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	resp, err := h.useCase.AssistantProxy(c.Request.Context(), actor.UserID, assistantUseCase.Call{Method: http.MethodGet, Path: path})
	if err != nil {
		response.AbortWithError(c, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if t := mediaType(resp.Header.Get("Content-Type")); t != "application/zip" && t != "application/octet-stream" {
		response.AbortWithError(c, wrongType(resp))
		return
	}
	// The type and the disposition are the platform's: only a cleaned file name is taken from the brain.
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": archiveName(resp.Header.Get("Content-Disposition"))}))
	c.Header("X-Content-Type-Options", "nosniff")
	privateNoStore(c)
	c.Header("Content-Type", "application/zip")
	c.Status(resp.StatusCode)
	_, _ = io.Copy(c.Writer, resp.Body)
	c.Abort()
}

const defaultArchiveName = "task.zip"

var unsafeNameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// archiveName is the file name of a Content-Disposition header, cleaned to a safe base name.
func archiveName(disposition string) string {
	_, params, err := mime.ParseMediaType(disposition)
	if err != nil {
		return defaultArchiveName
	}
	name := strings.ReplaceAll(params["filename"], "\\", "/")
	name = unsafeNameChars.ReplaceAllString(path.Base(name), "-")
	name = strings.Trim(name, ".-_")
	if len(name) > 100 {
		name = name[len(name)-100:]
		name = strings.Trim(name, ".-_")
	}
	if name == "" || name == "zip" {
		return defaultArchiveName
	}
	return name
}

func mediaType(header string) string {
	t, _, err := mime.ParseMediaType(header)
	if err != nil {
		return ""
	}
	return t
}

// wrongType is the error for a 2xx answer of a type the route does not take from the brain.
func wrongType(resp *http.Response) error {
	log.Warn().Str("type", mediaType(resp.Header.Get("Content-Type"))).Msg("Cyber-brain answered with an unexpected content type")
	return assistantModel.ErrAssistantUpstream.Err()
}

// events relays the session's event stream frame by frame, as the brain wrote it.
func (h *Handler) events(c *gin.Context) {
	path, ok := sessionPath(c, "/events")
	if !ok {
		return
	}
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	query := url.Values{}
	if after := c.Query("after"); after != "" {
		n, err := strconv.ParseUint(after, 10, 63)
		if err != nil {
			response.AbortWithBadRequest(c, errors.New("after must be a sequence number"))
			return
		}
		query.Set("after", strconv.FormatUint(n, 10))
	}
	release, admitted := sse.Streams.Acquire("assistant:"+actor.UserID.String(), limits.Get().StreamsPerUser)
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
	resp, err := h.useCase.AssistantProxy(streamCtx, actor.UserID, assistantUseCase.Call{
		Method: http.MethodGet, Path: path, Query: query, Header: http.Header{"Accept": {"text/event-stream"}},
	})
	if err != nil {
		response.AbortWithError(c, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if mediaType(resp.Header.Get("Content-Type")) != "text/event-stream" {
		response.AbortWithError(c, wrongType(resp))
		return
	}
	c.Header("X-Content-Type-Options", "nosniff")
	sse.WriteHeaders(c)
	frames := readFrames(streamCtx, resp.Body)
	heartbeat := time.NewTicker(sse.HeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-streamCtx.Done():
			return
		case frame, open := <-frames:
			if !open {
				return
			}
			// A frame is written whole, so a heartbeat never lands inside one.
			_, _ = c.Writer.Write(frame)
			flusher.Flush()
		case <-heartbeat.C:
			sse.Heartbeat(c.Writer, flusher)
		}
	}
}

// readFrames reads the stream into whole frames (the lines up to and including the blank line), unchanged. The
// channel closes when the brain ends the stream, a line is too long or the body fails; the caller's context cancel
// closes the body and so ends the reader.
func readFrames(ctx context.Context, body io.Reader) <-chan []byte {
	out := make(chan []byte)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 0, 64<<10), maxFrameBytes)
		sc.Split(scanLinesKeepingEnds)
		var frame []byte
		for sc.Scan() {
			line := sc.Bytes()
			frame = append(frame, line...)
			if len(bytes.TrimRight(line, "\r\n")) == 0 {
				select {
				case out <- frame:
				case <-ctx.Done():
					return
				}
				frame = nil
			}
		}
		if err := sc.Err(); err != nil && !errors.Is(err, context.Canceled) {
			log.Debug().Err(err).Msg("Assistant event stream ended with an error")
		}
	}()
	return out
}

// scanLinesKeepingEnds splits on \n and keeps it (a bufio.ScanLines drops it).
func scanLinesKeepingEnds(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i+1], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func privateNoStore(c *gin.Context) { c.Header("Cache-Control", "private, no-store") }

func contentTypeOr(resp *http.Response, fallback string) string {
	if v := resp.Header.Get("Content-Type"); v != "" {
		return v
	}
	return fallback
}
