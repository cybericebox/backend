package brainfake

import (
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	brainapiv1 "github.com/cybericebox/backend/pkg/brainapi/v1"
)

// DefaultMaxInflight is the max_inflight the fake's StreamHello advertises unless a test sets another.
const DefaultMaxInflight = 16

// Outbox is the brain's side of the outbox stream (Requests): the requests it still holds and what the platform answered. Like
// the brain it sends a StreamHello first, then every unanswered request; a reconnect re-sends every request that is still
// unanswered; an answer is taken by request_id from any stream and a duplicate is recorded but changes nothing; a new stream
// supersedes the one before (that one is ended).
type Outbox struct {
	mu          sync.Mutex
	maxInflight uint32
	noHello     bool
	pending     []*brainapiv1.BrainRequest
	answers     map[string][]*brainapiv1.RequestAnswer
	streams     int
	current     chan struct{} // closed when the current stream is superseded or dropped
	resend      int           // bumped to send every pending request again on the open stream
	lose        int           // how many of the next answers are lost (neither recorded nor taken)
	lost        int           // how many answers were lost so far
	wake        chan struct{}
}

// NewOutbox makes an empty outbox (New makes one for every fake).
func NewOutbox() *Outbox {
	return &Outbox{maxInflight: DefaultMaxInflight, answers: map[string][]*brainapiv1.RequestAnswer{}, wake: make(chan struct{})}
}

func (o *Outbox) wakeLocked() {
	close(o.wake)
	o.wake = make(chan struct{})
}

// Push adds requests; an open stream sends them at once.
func (o *Outbox) Push(reqs ...*brainapiv1.BrainRequest) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pending = append(o.pending, reqs...)
	o.wakeLocked()
}

// SetMaxInflight is the max_inflight of the next StreamHello.
func (o *Outbox) SetMaxInflight(n uint32) { o.mu.Lock(); defer o.mu.Unlock(); o.maxInflight = n }

// SkipHello makes the next streams start with a request instead of the StreamHello (a broken brain).
func (o *Outbox) SkipHello(skip bool) { o.mu.Lock(); defer o.mu.Unlock(); o.noHello = skip }

// Resend sends every unanswered request again on the open stream (a duplicate delivery).
func (o *Outbox) Resend() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.resend++
	o.wakeLocked()
}

// LoseAnswers makes the next n answers get lost on the way (the brain never sees them; their requests stay unanswered).
func (o *Outbox) LoseAnswers(n int) { o.mu.Lock(); defer o.mu.Unlock(); o.lose = n }

// Lost is how many answers were lost on the way so far (the brain has taken them off the stream and thrown them away).
func (o *Outbox) Lost() int { o.mu.Lock(); defer o.mu.Unlock(); return o.lost }

// DropStream ends the open stream (a network blip); the platform reconnects and gets the unanswered requests again.
func (o *Outbox) DropStream() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.current != nil {
		close(o.current)
		o.current = nil
	}
}

// Streams is how many outbox streams were opened.
func (o *Outbox) Streams() int { o.mu.Lock(); defer o.mu.Unlock(); return o.streams }

// Answers are the answers of a request, every copy that came.
func (o *Outbox) Answers(id string) []*brainapiv1.RequestAnswer {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]*brainapiv1.RequestAnswer(nil), o.answers[id]...)
}

// AllAnswers are the answers by request id.
func (o *Outbox) AllAnswers() map[string][]*brainapiv1.RequestAnswer {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make(map[string][]*brainapiv1.RequestAnswer, len(o.answers))
	for id, a := range o.answers {
		out[id] = append([]*brainapiv1.RequestAnswer(nil), a...)
	}
	return out
}

// Pending are the ids of the requests that have no answer yet.
func (o *Outbox) Pending() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	ids := make([]string, 0, len(o.pending))
	for _, r := range o.pending {
		ids = append(ids, r.GetRequestId())
	}
	return ids
}

// resolve closes a request without an answer on the stream (the brain does that for a stored task archive).
func (o *Outbox) resolve(id string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.removeLocked(id)
}

func (o *Outbox) removeLocked(id string) {
	for i, r := range o.pending {
		if r.GetRequestId() == id {
			o.pending = append(o.pending[:i], o.pending[i+1:]...)
			return
		}
	}
}

func (o *Outbox) serve(stream grpc.BidiStreamingServer[brainapiv1.RequestAnswer, brainapiv1.ServerFrame]) error {
	o.mu.Lock()
	o.streams++
	if o.current != nil {
		close(o.current) // the new stream supersedes the old one
	}
	mine := make(chan struct{})
	o.current = mine
	hello, inflight := !o.noHello, o.maxInflight
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		if o.current == mine {
			o.current = nil
		}
		o.mu.Unlock()
	}()
	if hello {
		if err := stream.Send(&brainapiv1.ServerFrame{Frame: &brainapiv1.ServerFrame_Hello{Hello: &brainapiv1.StreamHello{MaxInflight: inflight}}}); err != nil {
			return err
		}
	}
	recvErr := make(chan error, 1)
	go func() {
		for {
			a, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			o.mu.Lock()
			if o.lose > 0 {
				o.lose--
				o.lost++
				o.mu.Unlock()
				continue
			}
			o.answers[a.GetRequestId()] = append(o.answers[a.GetRequestId()], a)
			o.removeLocked(a.GetRequestId())
			o.mu.Unlock()
		}
	}()
	sent := map[string]bool{}
	resend := -1
	for {
		o.mu.Lock()
		if resend >= 0 && resend != o.resend {
			sent = map[string]bool{}
		}
		resend = o.resend
		var next []*brainapiv1.BrainRequest
		for _, r := range o.pending {
			if !sent[r.GetRequestId()] {
				sent[r.GetRequestId()] = true
				next = append(next, proto.CloneOf(r))
			}
		}
		wake := o.wake
		o.mu.Unlock()
		for _, r := range next {
			if err := stream.Send(&brainapiv1.ServerFrame{Frame: &brainapiv1.ServerFrame_Request{Request: r}}); err != nil {
				return err
			}
		}
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-mine:
			return nil
		case err := <-recvErr:
			return err
		case <-wake:
		}
	}
}
