package assistant

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	brainapiv1 "github.com/cybericebox/backend/pkg/brainapi/v1"
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
	// OutboxRequest is one request of the brain's outbox: its id, its kind (provide_lab, provide_device, provide_data,
	// provide_task) and the request itself for the handler of its kind.
	OutboxRequest struct {
		ID   string
		Type string
		Req  *brainapiv1.BrainRequest
	}

	// AnswerError is the refusal sent as the answer: a stable code and a human line.
	AnswerError struct {
		Code    string
		Message string
	}

	// requestHandler produces the result of one request kind (a RequestAnswer with its result set; the request id is filled
	// in when it is sent), or the refusal.
	requestHandler func(ctx context.Context, req OutboxRequest) (result *brainapiv1.RequestAnswer, refusal *AnswerError)

	// outboxStream is the outbox of this replica: the stream answers go up on, and the requests being handled (max_inflight of
	// the brain's StreamHello at most; a request the brain sends again while it is handled is not handled twice).
	outboxStream struct {
		mu       sync.Mutex
		send     func(*brainapiv1.RequestAnswer) error
		sendID   *int
		inflight map[string]*flight
		limit    int
		changed  chan struct{}
	}
)

// flight is a request being handled; resentOn is the stream a resend of it was skipped on (nil: none since the last answer).
type flight struct{ resentOn *int }

// requestIDPattern is what a request id may look like.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]{0,127}$`)

// errNoOutbox: no outbox stream is open, so an answer cannot be sent now (the caller tries again; the brain re-sends the
// request when the stream is back).
var errNoOutbox = errors.New("the cyber-brain outbox stream is not open")

// changedLocked wakes everyone waiting for a free slot or a stream.
func (o *outboxStream) changedLocked() {
	if o.changed != nil {
		close(o.changed)
	}
	o.changed = make(chan struct{})
}

func (o *outboxStream) wait() <-chan struct{} {
	if o.changed == nil {
		o.changed = make(chan struct{})
	}
	return o.changed
}

// openAs makes send (of the stream id) the one answers go up on, with the brain's max_inflight.
func (o *outboxStream) openAs(id *int, send func(*brainapiv1.RequestAnswer) error, limit int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.send, o.sendID, o.limit = send, id, limit
	o.changedLocked()
}

// close forgets the stream (only the one given: a newer stream stays).
func (o *outboxStream) close(id *int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.send != nil && o.sendID == id {
		o.send = nil
	}
	o.changedLocked()
}

// acquire takes a slot for a request that came on the given stream: it waits while max_inflight requests are being handled.
// duplicate is true when the request is being handled already (the brain sent it again); that resend is remembered, because
// the answer of the handling may have gone up a stream the brain has left, and then nothing else would answer the request.
func (o *outboxStream) acquire(ctx context.Context, id string, stream *int) (duplicate bool, err error) {
	for {
		o.mu.Lock()
		if o.inflight == nil {
			o.inflight = map[string]*flight{}
		}
		if f, busy := o.inflight[id]; busy {
			f.resentOn = stream
			o.mu.Unlock()
			return true, nil
		}
		if len(o.inflight) < max(o.limit, 1) {
			o.inflight[id] = &flight{}
			o.mu.Unlock()
			return false, nil
		}
		ch := o.wait()
		o.mu.Unlock()
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ch:
		}
	}
}

// finish ends the handling of a request whose answer went up the stream sentOn (failed: it did not get through). It answers
// true when the request has to be answered again, because a resend was skipped meanwhile and the answer either failed or went
// up another stream than the resend came on; otherwise it frees the slot.
func (o *outboxStream) finish(id string, sentOn *int, failed bool) (again bool) {
	o.mu.Lock()
	if f := o.inflight[id]; f != nil && f.resentOn != nil && (failed || f.resentOn != sentOn) {
		f.resentOn = nil
		o.mu.Unlock()
		return true
	}
	o.mu.Unlock()
	o.release(id)
	return false
}

func (o *outboxStream) release(id string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.inflight, id)
	o.changedLocked()
}

// sendAnswer sends on the open stream (sentOn is its id); errNoOutbox while there is none.
func (o *outboxStream) sendAnswer(a *brainapiv1.RequestAnswer) (next <-chan struct{}, sentOn *int, err error) {
	o.mu.Lock()
	send, ch, on := o.send, o.wait(), o.sendID
	o.mu.Unlock()
	if send == nil {
		return ch, nil, errNoOutbox
	}
	return ch, on, send(a)
}

// claimTurns lets the lab claims (provide_lab, provide_device) run one at a time in the order the stream delivered them: a
// ticket is taken when the request is read, the handler waits for its turn and passes it on when it is done.
type claimTurns struct {
	mu   sync.Mutex
	cond *sync.Cond
	next uint64 // the next ticket to hand out
	turn uint64 // the ticket whose turn it is
}

func (c *claimTurns) init() {
	if c.cond == nil {
		c.cond = sync.NewCond(&c.mu)
	}
}

// take hands out the next ticket as a context the handler carries.
func (c *claimTurns) take(ctx context.Context) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	ticketed := context.WithValue(ctx, claimTicketKey{}, c.next)
	c.next++
	return ticketed
}

type claimTicketKey struct{}

// wait blocks until the ticket of ctx has its turn (a context without a ticket goes at once).
func (c *claimTurns) wait(ctx context.Context) {
	ticket, ok := ctx.Value(claimTicketKey{}).(uint64)
	if !ok {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	for c.turn != ticket {
		c.cond.Wait()
	}
}

// done passes the turn on (a context without a ticket changes nothing).
func (c *claimTurns) done(ctx context.Context) {
	if _, ok := ctx.Value(claimTicketKey{}).(uint64); !ok {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	c.turn++
	c.cond.Broadcast()
}

// requestKind names the kind of a request ("" when the brain sent a kind this platform does not know).
func requestKind(r *brainapiv1.BrainRequest) string {
	switch r.GetKind().(type) {
	case *brainapiv1.BrainRequest_ProvideLab:
		return "provide_lab"
	case *brainapiv1.BrainRequest_ProvideDevice:
		return "provide_device"
	case *brainapiv1.BrainRequest_ProvideData:
		return "provide_data"
	case *brainapiv1.BrainRequest_ProvideTask:
		return "provide_task"
	}
	return ""
}

// handlers maps the request kinds the platform knows to their handlers. A kind that is not here is answered with
// unsupported_request.
func (u *UseCase) handlers() map[string]requestHandler {
	return map[string]requestHandler{
		"provide_lab":    u.provideLab,
		"provide_device": u.provideDevice,
		"provide_data":   u.provideData,
		"provide_task":   u.provideTask,
	}
}

// toolsDeviceType is the neutral device type of the platform tools device; configured_host (a host the author set up)
// is not provided yet.
const toolsDeviceType = "tools"

// provideDevice answers provide_device. The tools device is provided as the task's lab with the platform tools device in
// it (see provideTaskLab); a configured host, a draft, or a platform without a tools image is not_supported_yet.
func (u *UseCase) provideDevice(ctx context.Context, req OutboxRequest) (*brainapiv1.RequestAnswer, *AnswerError) {
	in := req.Req.GetProvideDevice()
	if in.GetDeviceType() != toolsDeviceType {
		return nil, &AnswerError{Code: codeNotSupportedYet, Message: "this device type is not supported yet"}
	}
	return u.provideTaskLab(ctx, req.ID, in.GetSubject(), in.GetSource(), true)
}

const (
	watchBackoffMin = time.Second
	watchBackoffMax = time.Minute
	// watchStable: a stream that lived this long resets the backoff.
	watchStable = 30 * time.Second
	// answerAttempts is how often an answer is tried (waiting for an open stream in between) before the caller is told it
	// did not get through; the brain re-sends a request that stays unanswered.
	answerAttempts = 5
	// defaultInflight is the max_inflight of a StreamHello that names none (the contract suggests 16).
	defaultInflight = 16
)

// WatchOutbox holds the brain's outbox stream (Requests) until ctx ends (the leadership is lost, or the process stops): it
// reconnects with an exponential backoff and jitter. There is no cursor: the brain re-sends every unanswered request on a new
// stream, and a new leader's stream supersedes the old one. Only the leader replica runs it.
func (u *UseCase) WatchOutbox(ctx context.Context) {
	if !u.Enabled() {
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

// watchOnce holds one stream: the StreamHello first, then every request is handled (at most max_inflight at once: the stream
// is not read while they are all taken) and answered on the open stream.
func (u *UseCase) watchOnce(ctx context.Context) error {
	c, err := u.control(ctx)
	if err != nil {
		return err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := c.Requests(streamCtx)
	if err != nil {
		return err
	}
	first, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("read the outbox hello: %w", err)
	}
	hello := first.GetHello()
	if hello == nil {
		return errors.New("the cyber-brain outbox stream did not start with a StreamHello")
	}
	limit := int(hello.GetMaxInflight())
	if limit <= 0 {
		limit = defaultInflight
	}
	var sendMu sync.Mutex
	id := new(int)
	u.outbox.openAs(id, func(a *brainapiv1.RequestAnswer) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(a)
	}, limit)
	defer u.outbox.close(id)
	for {
		frame, recvErr := stream.Recv()
		if recvErr != nil {
			return recvErr
		}
		req := frame.GetRequest()
		if req == nil {
			continue // a second hello: nothing to do
		}
		if !requestIDPattern.MatchString(req.GetRequestId()) {
			log.Warn().Msg("A cyber-brain outbox request without a usable request_id was skipped: nothing can answer it")
			continue
		}
		duplicate, waitErr := u.outbox.acquire(streamCtx, req.GetRequestId(), id)
		if waitErr != nil {
			return waitErr
		}
		if duplicate {
			continue // re-sent while it is being handled: one handling answers it
		}
		handleCtx := ctx
		if kind := requestKind(req); kind == "provide_lab" || kind == "provide_device" {
			handleCtx = u.claims.take(ctx)
		}
		go u.handleRequest(handleCtx, req)
	}
}

// handleRequest answers one request on the open stream and frees its slot. A request that cannot be answered now stays
// unanswered at the brain, which sends it again; every handler is a pure read, a refusal, or idempotent by request id.
func (u *UseCase) handleRequest(ctx context.Context, r *brainapiv1.BrainRequest) {
	finished := false
	defer func() {
		if !finished {
			u.outbox.release(r.GetRequestId())
		}
	}()
	// A lab claim waits for its turn; the turn is passed on whatever the handler does.
	u.claims.wait(ctx)
	defer u.claims.done(ctx)
	req := OutboxRequest{ID: r.GetRequestId(), Type: requestKind(r), Req: r}
	var result *brainapiv1.RequestAnswer
	var refusal *AnswerError
	if handler, ok := u.handlers()[req.Type]; ok {
		result, refusal = u.safely(ctx, handler, req)
	} else {
		refusal = &AnswerError{Code: codeUnsupportedRequest, Message: "this request type is not supported"}
	}
	if refusal == answerLater {
		return // the handler persisted the request as claimed and answers it itself
	}
	for {
		sentOn, err := u.answerOn(ctx, req.ID, result, refusal)
		if err != nil {
			log.Warn().Err(err).Str("request", req.ID).Str("type", req.Type).Msg("An outbox answer did not get through; the brain sends the request again")
		}
		// A resend of the request skipped while it was handled is answered now when the answer did not go up its stream.
		if !u.outbox.finish(req.ID, sentOn, err != nil) {
			finished = true
			return
		}
	}
}

// safely runs a handler so that a failure of one request never takes the stream down: a panic becomes an internal refusal.
func (u *UseCase) safely(ctx context.Context, h requestHandler, req OutboxRequest) (result *brainapiv1.RequestAnswer, refusal *AnswerError) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Str("type", req.Type).Msg("An outbox request handler panicked")
			result, refusal = nil, &AnswerError{Code: codeInternal, Message: "the platform could not handle the request"}
		}
	}()
	return h(ctx, req)
}

// answer sends the result or the refusal of a request up the open outbox stream (any stream: the brain takes an answer by
// request_id and ignores a duplicate). While no stream is open, or a send fails, it waits for the next stream and tries again,
// a few times; then the error is returned and the brain, which still holds the request, sends it again.
func (u *UseCase) answer(ctx context.Context, requestID string, result *brainapiv1.RequestAnswer, refusal *AnswerError) error {
	_, err := u.answerOn(ctx, requestID, result, refusal)
	return err
}

// answerOn is answer that also tells the stream the answer went up.
func (u *UseCase) answerOn(ctx context.Context, requestID string, result *brainapiv1.RequestAnswer, refusal *AnswerError) (*int, error) {
	msg := &brainapiv1.RequestAnswer{RequestId: requestID}
	switch {
	case refusal != nil:
		msg.Result = &brainapiv1.RequestAnswer_Error{Error: &brainapiv1.RequestError{Code: refusal.Code, Message: refusal.Message}}
	case result != nil:
		msg.Result = result.GetResult()
	}
	pause := u.answerPause
	var err error
	for attempt := 1; ; attempt++ {
		var next <-chan struct{}
		var sentOn *int
		if next, sentOn, err = u.outbox.sendAnswer(msg); err == nil {
			return sentOn, nil
		}
		if attempt >= answerAttempts {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-next: // a stream opened or closed
		case <-time.After(pause):
		}
		pause *= 2
	}
}
