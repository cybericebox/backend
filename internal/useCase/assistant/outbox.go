package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/cybericebox/backend/internal/delivery/infrastructure/brain"
)

// The codes the platform answers an outbox request with when it cannot give a result. The schema is additive: the brain
// treats a code it does not know as a plain refusal.
const (
	codeUnsupportedRequest = "unsupported_request"
	codeNotSupportedYet    = "not_supported_yet"
	codeCollectionDisabled = "collection_disabled"
	codeInvalidRequest     = "invalid_request"
	codeInternal           = "internal"
)

type (
	// OutboxCursor keeps the last outbox request that was answered, so a restart or a new leader resumes after it.
	OutboxCursor interface {
		OutboxSeq(ctx context.Context, endpoint string) (int64, error)
		SetOutboxSeq(ctx context.Context, endpoint string, seq int64) error
	}

	// OutboxRequest is one request of the brain's outbox: `{request_id, type, ...}`; Raw is the whole payload for the
	// handler of its type.
	OutboxRequest struct {
		Seq  int64
		ID   string          `json:"request_id"`
		Type string          `json:"type"`
		Raw  json.RawMessage `json:"-"`
	}

	// AnswerError is the refusal sent as the answer: a stable code and a human line.
	AnswerError struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}

	// requestHandler produces the result of one request type, or the refusal.
	requestHandler func(ctx context.Context, req OutboxRequest) (result any, refusal *AnswerError)
)

// requestIDPattern is what a request id may look like in a path.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]{0,127}$`)

// requestIDField finds a request_id in a payload that is not valid JSON.
var requestIDField = regexp.MustCompile(`"request_id"\s*:\s*"([A-Za-z0-9][A-Za-z0-9._~-]{0,127})"`)

// readableRequestID is the request id of a payload, read leniently: from the JSON when it parses, else from the text.
// Empty when there is none.
func readableRequestID(data []byte) string {
	var probe struct {
		ID string `json:"request_id"`
	}
	if json.Unmarshal(data, &probe) == nil && requestIDPattern.MatchString(probe.ID) {
		return probe.ID
	}
	if m := requestIDField.FindSubmatch(data); m != nil {
		return string(m[1])
	}
	return ""
}

// handlers maps the request types the platform knows to their handlers. A type that is not here is answered with
// unsupported_request.
func (u *UseCase) handlers() map[string]requestHandler {
	return map[string]requestHandler{
		"provide_lab":    u.provideLab,
		"provide_device": u.provideDevice,
		"provide_data":   u.provideData,
	}
}

// toolsDeviceType is the neutral device type of the platform tools device; configured_host (a host the author set up)
// is not provided yet.
const toolsDeviceType = "tools"

// provideDevice answers provide_device. The tools device is provided as the task's lab with the platform tools device in
// it (see provideTaskLab); a configured host, a draft, or a platform without a tools image is not_supported_yet.
func (u *UseCase) provideDevice(ctx context.Context, req OutboxRequest) (any, *AnswerError) {
	var in struct {
		DeviceType string `json:"device_type"`
	}
	if err := json.Unmarshal(req.Raw, &in); err != nil {
		return nil, &AnswerError{Code: codeInvalidRequest, Message: "the request could not be read"}
	}
	if in.DeviceType != toolsDeviceType {
		return nil, &AnswerError{Code: codeNotSupportedYet, Message: "this device type is not supported yet"}
	}
	return u.provideTaskLab(ctx, req, true)
}

const (
	watchBackoffMin = time.Second
	watchBackoffMax = time.Minute
	// watchStable: a stream that lived this long resets the backoff.
	watchStable = 30 * time.Second
	// answerAttempts is how often an answer is sent before the stream is dropped (the cursor stays, the brain re-sends).
	answerAttempts = 5
)

// WatchOutbox holds the brain's outbox stream until ctx ends (the leadership is lost, or the process stops): it
// reconnects with an exponential backoff and jitter, and resumes after the last answered request. Only the leader
// replica runs it.
func (u *UseCase) WatchOutbox(ctx context.Context) {
	if !u.Enabled() || u.cursor == nil {
		return
	}
	backoff := u.watchMin
	for ctx.Err() == nil {
		started := u.now()
		err := u.watchOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Warn().Err(err).Msg("The cyber-brain outbox stream ended")
		}
		if u.now().Sub(started) >= watchStable {
			backoff = u.watchMin
		}
		// ±20 % jitter, so replicas that lost the leader together do not come back together.
		wait := time.Duration(float64(backoff) * (0.8 + 0.4*rand.Float64())) //nolint:gosec // jitter, not a secret
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		backoff = min(backoff*2, u.watchMax)
	}
}

// watchOnce holds one stream: every request after the cursor is handled, answered and then the cursor moves past it.
func (u *UseCase) watchOnce(ctx context.Context) error {
	after, err := u.cursor.OutboxSeq(ctx, u.endpoint())
	if err != nil {
		return fmt.Errorf("read the outbox cursor: %w", err)
	}
	c, err := u.client(ctx)
	if err != nil {
		return err
	}
	resp, err := c.Do(ctx, brain.Request{
		Method: http.MethodGet, Path: "/v1/requests/watch", Query: url.Values{"after": {strconv.FormatInt(after, 10)}},
		Header: http.Header{"Accept": {"text/event-stream"}},
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return brain.ReadFrames(resp.Body, func(f brain.Frame) error {
		seq, perr := strconv.ParseInt(f.ID, 10, 64)
		if perr != nil || seq <= 0 {
			return nil // a heartbeat or a frame without a sequence number
		}
		if seq <= after {
			return nil // seen already
		}
		if err := u.handleRequest(ctx, seq, f); err != nil {
			return err
		}
		after = seq
		return nil
	})
}

// handleRequest answers one request and then moves the cursor past it. The cursor moves only after the answer got
// through (or the brain said the request is gone), so a request is never skipped; one that was answered but whose cursor
// write failed is sent again, which is harmless: the answer is idempotent by request_id at the brain and every handler
// is a pure read or a refusal.
func (u *UseCase) handleRequest(ctx context.Context, seq int64, f brain.Frame) error {
	req := OutboxRequest{Seq: seq, Raw: json.RawMessage(f.Data)}
	if err := json.Unmarshal(f.Data, &req); err != nil || !requestIDPattern.MatchString(req.ID) {
		// A broken request still gets its answer when its id can be read, or the brain would keep it open for ever.
		id := readableRequestID(f.Data)
		if id == "" {
			log.Warn().Int64("seq", seq).Msg("A cyber-brain outbox request without a readable request_id was skipped")
			return u.cursor.SetOutboxSeq(ctx, u.endpoint(), seq)
		}
		log.Warn().Int64("seq", seq).Str("request", id).Msg("A malformed cyber-brain outbox request is answered as invalid")
		refusal := &AnswerError{Code: codeInvalidRequest, Message: "the request could not be read"}
		if err = u.answer(ctx, id, nil, refusal); err != nil {
			return fmt.Errorf("answer request %s: %w", id, err)
		}
		return u.cursor.SetOutboxSeq(ctx, u.endpoint(), seq)
	}
	if req.Type == "" {
		req.Type = f.Event
	}
	var result any
	var refusal *AnswerError
	if handler, ok := u.handlers()[req.Type]; ok {
		result, refusal = u.safely(ctx, handler, req)
	} else {
		refusal = &AnswerError{Code: codeUnsupportedRequest, Message: "this request type is not supported"}
	}
	if refusal == answerLater {
		// The handler persisted the request as claimed and answers it itself: the stream goes on.
		return u.cursor.SetOutboxSeq(ctx, u.endpoint(), seq)
	}
	if err := u.answer(ctx, req.ID, result, refusal); err != nil {
		return fmt.Errorf("answer request %s: %w", req.ID, err)
	}
	return u.cursor.SetOutboxSeq(ctx, u.endpoint(), seq)
}

// safely runs a handler so that a failure of one request never takes the stream down: a panic or an error becomes an
// internal refusal.
func (u *UseCase) safely(ctx context.Context, h requestHandler, req OutboxRequest) (result any, refusal *AnswerError) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Str("type", req.Type).Msg("An outbox request handler panicked")
			result, refusal = nil, &AnswerError{Code: codeInternal, Message: "the platform could not handle the request"}
		}
	}()
	return h(ctx, req)
}

// answer sends `{result}` or `{error: {code, message}}` to the brain, retried on a 5xx, a 429 and network failures.
// A 4xx that says the request is gone or already answered (400, 404, 409, 410, 422) is final: there is nothing to
// retry. Any other failure is returned and the stream is dropped; the brain sends the request again.
func (u *UseCase) answer(ctx context.Context, requestID string, result any, refusal *AnswerError) error {
	var body []byte
	var err error
	if refusal != nil {
		body, err = json.Marshal(map[string]*AnswerError{"error": refusal})
	} else {
		body, err = json.Marshal(map[string]any{"result": result})
	}
	if err != nil {
		return err
	}
	c, err := u.client(ctx)
	if err != nil {
		return err
	}
	pause := u.answerPause
	for attempt := 1; ; attempt++ {
		resp, doErr := c.Do(ctx, brain.Request{
			Method: http.MethodPost, Path: "/v1/requests/" + requestID + "/answer",
			Header: http.Header{"Content-Type": {"application/json"}}, Body: bytes.NewReader(body), ContentLength: int64(len(body)),
		})
		if doErr == nil {
			_ = resp.Body.Close()
			return nil
		}
		var api *brain.APIError
		if errors.As(doErr, &api) {
			switch api.Status {
			case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusGone, http.StatusUnprocessableEntity:
				log.Warn().Int("status", api.Status).Str("code", api.Code).Msg("The brain refused an answer as final")
				return nil
			}
			if api.Status < 500 && api.Status != http.StatusTooManyRequests {
				return doErr
			}
		} else if !errors.Is(doErr, brain.ErrUnreachable) {
			return doErr
		}
		if attempt >= answerAttempts {
			return doErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pause):
		}
		pause *= 2
	}
}
