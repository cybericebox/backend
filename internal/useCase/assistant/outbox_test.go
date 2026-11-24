package assistant_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/cybericebox/backend/internal/delivery/infrastructure/brain/brainfake"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	"github.com/cybericebox/backend/internal/useCase/assistant"
	brainapiv1 "github.com/cybericebox/backend/pkg/brainapi/v1"
)

// outboxFrame is one request of the brain's outbox, written as the JSON of its kind: {"request_id", "type", ...the fields of the
// kind} (provide_data names its tasks "task_refs"). It becomes a BrainRequest when the outbox serves it.
type outboxFrame struct {
	seq   int64
	event string
	data  string
}

// toRequest turns a frame into the BrainRequest of its type; an unknown type is a request of a kind the platform does not
// know (no kind set).
func toRequest(t *testing.T, f outboxFrame) *brainapiv1.BrainRequest {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(f.data), &fields); err != nil {
		t.Fatalf("frame %q: %v", f.data, err)
	}
	var id, typ string
	_ = json.Unmarshal(fields["request_id"], &id)
	_ = json.Unmarshal(fields["type"], &typ)
	delete(fields, "request_id")
	delete(fields, "type")
	if typ == "" {
		typ = f.event
	}
	if refs, ok := fields["task_refs"]; ok {
		fields["tasks"] = refs
		delete(fields, "task_refs")
	}
	body, _ := json.Marshal(fields)
	req := &brainapiv1.BrainRequest{}
	switch typ {
	case "provide_lab", "provide_device", "provide_data", "provide_task":
		wrapped, _ := json.Marshal(map[string]json.RawMessage{"request_id": json.RawMessage(fmt.Sprintf("%q", id)), typ: body})
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(wrapped, req); err != nil {
			t.Fatalf("frame %q: %v", f.data, err)
		}
	default:
		req.RequestId = id
	}
	return req
}

// outboxBrain is the brain's side of the outbox in the tests: the frames it serves (pushed to the fake's outbox when the
// watcher starts, or at once when it runs) and what the platform answered, as proto-JSON.
type outboxBrain struct {
	t      *testing.T
	fake   *brainfake.Fake
	mu     sync.Mutex
	frames []outboxFrame
	pushed int
}

// push hands the frames not pushed yet to the fake's outbox.
func (b *outboxBrain) push() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, f := range b.frames[b.pushed:] {
		b.fake.Outbox.Push(toRequest(b.t, f))
	}
	b.pushed = len(b.frames)
}

// add serves more frames on the open stream.
func (b *outboxBrain) add(frames ...outboxFrame) {
	b.mu.Lock()
	b.frames = append(b.frames, frames...)
	b.mu.Unlock()
	b.push()
}

func (b *outboxBrain) watchCount() int { return b.fake.Outbox.Streams() }

// answered are the answers of a request as proto-JSON: {"request_id", "device"|"data_json"|"task_uploaded"|"error"}.
func (b *outboxBrain) answered(id string) []string {
	var out []string
	for _, a := range b.fake.Outbox.Answers(id) {
		out = append(out, string(brainfake.WireJSON(a)))
	}
	return out
}

// uploaded is the task archive stored for a request (none or one: a repeat is AlreadyExists).
func (b *outboxBrain) uploaded(id string) []*brainfake.Archive {
	if a := b.fake.Archives.Uploaded(id); a != nil {
		return []*brainfake.Archive{a}
	}
	return nil
}

// down breaks the outbox: the open stream ends and every new one is unusable (it does not start with the hello), so no answer
// gets through.
func (b *outboxBrain) down() {
	before := b.fake.Outbox.Streams()
	b.fake.Outbox.SkipHello(true)
	b.fake.Outbox.DropStream()
	// The platform has seen the end of the stream once it opens another (a send on a stream the brain has ended may still look
	// sent to the platform: that is why the brain re-sends every unanswered request).
	eventually(b.t, "the platform to reconnect", func() bool { return b.fake.Outbox.Streams() > before })
}

type outboxRig struct {
	*rig
	brain *outboxBrain
}

func newOutboxRig(t *testing.T, frames ...outboxFrame) *outboxRig {
	t.Helper()
	return newOutboxRigWith(t, nil, frames...)
}

func newOutboxRigWith(t *testing.T, mutate func(*assistant.Dependencies), frames ...outboxFrame) *outboxRig {
	t.Helper()
	r := newRig(t, func(d *assistant.Dependencies) {
		d.WatchMin, d.WatchMax, d.AnswerPause = 5*time.Millisecond, 20*time.Millisecond, time.Millisecond
		if mutate != nil {
			mutate(d)
		}
	})
	if _, err := r.uc.BootstrapBrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &outboxRig{rig: r, brain: &outboxBrain{t: t, fake: r.fake, frames: frames}}
}

// run holds the outbox until stop is called (the leadership lost).
func (o *outboxRig) run(t *testing.T) (stop func()) {
	t.Helper()
	o.brain.push()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); o.uc.WatchOutbox(ctx) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("the outbox watcher did not stop with its context")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func req(seq int64, id, typ string) outboxFrame {
	return outboxFrame{seq: seq, event: typ, data: fmt.Sprintf(`{"request_id":%q,"type":%q}`, id, typ)}
}

func refusalCode(t *testing.T, body string) string {
	t.Helper()
	var got struct {
		Error *struct{ Code, Message string }
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || got.Error == nil || got.Error.Message == "" {
		t.Fatalf("not a refusal with a code and a message: %q %v", body, err)
	}
	return got.Error.Code
}

func TestALabFromADraftAndAnyDeviceAreAnsweredNotSupportedYet(t *testing.T) {
	draft := outboxFrame{event: "provide_lab", data: `{"request_id":"r1","type":"provide_lab","subject":"x","source":{"draft":"d-1"}}`}
	device := outboxFrame{event: "provide_device", data: `{"request_id":"r2","type":"provide_device","device_type":"configured_host"}`}
	o := newOutboxRig(t, draft, device)
	o.run(t)
	eventually(t, "both answered", func() bool { return len(o.brain.answered("r1")) == 1 && len(o.brain.answered("r2")) == 1 })
	for _, id := range []string{"r1", "r2"} {
		if answers := o.brain.answered(id); refusalCode(t, answers[0]) != "not_supported_yet" {
			t.Fatalf("%s: %v", id, answers)
		}
	}
}

func TestARequestOfAnUnknownKindIsRefusedAndTheStreamGoesOn(t *testing.T) {
	o := newOutboxRig(t, req(1, "r1", "teleport"), req(2, "r2", "provide_lab"))
	o.run(t)
	eventually(t, "both answered", func() bool { return len(o.brain.answered("r1")) == 1 && len(o.brain.answered("r2")) == 1 })
	if a := o.brain.answered("r1"); refusalCode(t, a[0]) != "unsupported_request" {
		t.Fatalf("unknown kind: %v", a)
	}
}

func TestARequestWithoutAUsableIDIsSkippedAndTheStreamGoesOn(t *testing.T) {
	o := newOutboxRig(t, req(1, "", "provide_lab"), req(2, "bad/id with spaces", "provide_lab"), req(3, "r3", "provide_lab"))
	o.run(t)
	eventually(t, "the request after the unusable ones", func() bool { return len(o.brain.answered("r3")) == 1 })
	if all := o.brain.fake.Outbox.AllAnswers(); len(all) != 1 {
		t.Fatalf("nothing can answer a request without an id: %v", all)
	}
}

// The first frame of the stream is the StreamHello; a stream that starts otherwise is dropped and opened again.
func TestAStreamThatDoesNotStartWithTheHelloIsDroppedAndReopened(t *testing.T) {
	o := newOutboxRig(t, req(1, "r1", "provide_lab"))
	o.brain.fake.Outbox.SkipHello(true)
	o.run(t)
	eventually(t, "several streams", func() bool { return o.brain.watchCount() >= 3 })
	if len(o.brain.answered("r1")) != 0 {
		t.Fatal("nothing is handled on a stream without the hello")
	}
	o.brain.fake.Outbox.SkipHello(false)
	eventually(t, "answered on a proper stream", func() bool { return len(o.brain.answered("r1")) == 1 })
}

// A dropped stream is opened again and the brain re-sends what it has no answer for; there is no cursor.
func TestUnansweredRequestsAreHandledAgainAfterAReconnect(t *testing.T) {
	o := newOutboxRig(t, req(1, "r1", "teleport"))
	o.run(t)
	eventually(t, "answered", func() bool { return len(o.brain.answered("r1")) == 1 })
	o.brain.fake.Outbox.DropStream()
	eventually(t, "a second stream", func() bool { return o.brain.watchCount() >= 2 })
	o.brain.add(req(2, "r2", "teleport"))
	eventually(t, "the next request on the new stream", func() bool { return len(o.brain.answered("r2")) == 1 })
	if n := len(o.brain.answered("r1")); n != 1 {
		t.Fatalf("an answered request is not sent again by the brain, so not answered again: %d", n)
	}
}

// blockingStats is a statistics source whose reads wait until released, counting the reads in progress.
type blockingStats struct {
	release chan struct{}
	now     atomic.Int32
	most    atomic.Int32
	calls   atomic.Int32
}

func (b *blockingStats) Name() string { return "events" }
func (b *blockingStats) Aggregates(context.Context, []assistantModel.TaskRef, time.Time) ([]assistantModel.Aggregate, error) {
	return nil, nil
}
func (b *blockingStats) CollectionOn(_ context.Context, _ []assistantModel.TaskRef) (map[assistantModel.TaskRef]bool, error) {
	b.calls.Add(1)
	n := b.now.Add(1)
	for {
		most := b.most.Load()
		if n <= most || b.most.CompareAndSwap(most, n) {
			break
		}
	}
	<-b.release
	b.now.Add(-1)
	return map[assistantModel.TaskRef]bool{}, nil
}

func dataFrame(id string) outboxFrame {
	return outboxFrame{event: "provide_data", data: fmt.Sprintf(`{"request_id":%q,"type":"provide_data","task_refs":[{"task_ref":"t","version":"v","image":""}]}`, id)}
}

// At most max_inflight requests of the StreamHello are handled at once; the stream is read on as slots free.
func TestTheOutboxRespectsMaxInflight(t *testing.T) {
	stats := &blockingStats{release: make(chan struct{})}
	o := newOutboxRigWith(t, func(d *assistant.Dependencies) { d.Stats = stats }, dataFrame("d1"), dataFrame("d2"), dataFrame("d3"), dataFrame("d4"), dataFrame("d5"))
	o.brain.fake.Outbox.SetMaxInflight(2)
	o.run(t)
	eventually(t, "two requests in progress", func() bool { return stats.now.Load() == 2 })
	time.Sleep(40 * time.Millisecond)
	if stats.calls.Load() != 2 {
		t.Fatalf("a third request is not taken while two are in progress: %d", stats.calls.Load())
	}
	close(stats.release)
	eventually(t, "all five answered", func() bool {
		n := 0
		for _, id := range []string{"d1", "d2", "d3", "d4", "d5"} {
			n += len(o.brain.answered(id))
		}
		return n == 5
	})
	if most := stats.most.Load(); most > 2 {
		t.Fatalf("max_inflight 2, %d were handled at once", most)
	}
}

// A request the brain sends again while it is being handled (on the same stream, or on a new one) is handled once.
func TestARequestResentWhileInFlightIsHandledOnce(t *testing.T) {
	stats := &blockingStats{release: make(chan struct{})}
	o := newOutboxRigWith(t, func(d *assistant.Dependencies) { d.Stats = stats }, dataFrame("d1"))
	o.run(t)
	eventually(t, "the request in progress", func() bool { return stats.now.Load() == 1 })
	o.brain.fake.Outbox.Resend()
	time.Sleep(20 * time.Millisecond)
	o.brain.fake.Outbox.DropStream()
	eventually(t, "a second stream", func() bool { return o.brain.watchCount() >= 2 })
	time.Sleep(40 * time.Millisecond)
	close(stats.release)
	eventually(t, "answered", func() bool { return len(o.brain.answered("d1")) >= 1 })
	time.Sleep(30 * time.Millisecond)
	if n := stats.calls.Load(); n != 1 {
		t.Fatalf("handled %d times", n)
	}
	if n := len(o.brain.answered("d1")); n != 1 {
		t.Fatalf("answered %d times", n)
	}
}

// An answer whose handler finished while no stream was open goes up the next stream (answers are taken on any stream).
func TestAnAnswerWaitsForTheNextStream(t *testing.T) {
	stats := &blockingStats{release: make(chan struct{})}
	o := newOutboxRigWith(t, func(d *assistant.Dependencies) {
		d.Stats = stats
		d.WatchMin, d.WatchMax, d.AnswerPause = 20*time.Millisecond, 40*time.Millisecond, 5*time.Millisecond
	}, dataFrame("d1"))
	o.run(t)
	eventually(t, "the request in progress", func() bool { return stats.now.Load() == 1 })
	o.brain.fake.Outbox.DropStream()
	close(stats.release)
	// A copy of the answer may have gone up the old stream just before it ended and be recorded too; the brain ignores a duplicate.
	eventually(t, "answered after the reconnect", func() bool { return len(o.brain.answered("d1")) >= 1 })
}

// The answer went up a stream the brain then ended and was lost; the brain's resend on the new stream comes while the handling
// has not freed its slot yet, so it is skipped as a duplicate. The handling must answer again on the new stream.
func TestAResendSkippedWhileTheLostAnswerIsStillBeingHandledIsAnsweredOnTheNewStream(t *testing.T) {
	stats := &blockingStats{release: make(chan struct{})}
	close(stats.release)
	o := newOutboxRigWith(t, func(d *assistant.Dependencies) { d.Stats = stats }, dataFrame("d1"))
	answered, hold := make(chan struct{}), make(chan struct{})
	var first sync.Once
	o.uc.AfterAnswer(func(string) { first.Do(func() { close(answered); <-hold }) })
	o.brain.fake.Outbox.LoseAnswers(1)
	o.run(t)
	<-answered // the answer is sent (and lost); the slot is still held
	eventually(t, "the answer to be lost", func() bool { return o.brain.fake.Outbox.Lost() == 1 })
	o.brain.fake.Outbox.DropStream()
	eventually(t, "the resend on the new stream to be skipped", func() bool { return o.uc.ResendSkipped("d1") })
	close(hold)
	eventually(t, "answered on the new stream", func() bool { return len(o.brain.answered("d1")) >= 1 })
}

func TestTheWatcherStopsWhenLeadershipIsLost(t *testing.T) {
	o := newOutboxRig(t)
	stop := o.run(t)
	eventually(t, "the first connection", func() bool { return o.brain.watchCount() >= 1 })
	stop() // fails the test itself if the watcher does not return
	seen := o.brain.watchCount()
	time.Sleep(60 * time.Millisecond)
	if o.brain.watchCount() != seen {
		t.Fatal("the watcher reconnected after it was stopped")
	}
}

func TestTheWatcherReconnectsAfterTheBrainDropsTheStream(t *testing.T) {
	o := newOutboxRig(t)
	o.run(t)
	for i := 1; i <= 3; i++ {
		eventually(t, "a connection", func() bool { return o.brain.watchCount() >= i })
		time.Sleep(5 * time.Millisecond)
		o.brain.fake.Outbox.DropStream()
	}
}

// A new stream (a new leader's) supersedes the old one: the brain ends the old stream and the requests go to the new one.
func TestANewLeadersStreamSupersedesTheOldOne(t *testing.T) {
	a := newOutboxRig(t)
	stopA := a.run(t)
	eventually(t, "the first leader's stream", func() bool { return a.brain.watchCount() == 1 })
	stopA() // the leadership moves
	b := assistant.New(assistant.Dependencies{Store: a.store, Cipher: cipher(t, keyA), Config: assistant.Config{Endpoint: a.fake.Endpoint(), GRPCEndpoint: a.fake.GRPCEndpoint()}, Now: a.clock.now})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); b.WatchOutbox(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	eventually(t, "the new leader's stream", func() bool { return a.brain.watchCount() >= 2 })
	a.brain.add(req(1, "r1", "teleport"))
	eventually(t, "answered by the new leader", func() bool { return len(a.brain.answered("r1")) == 1 })
}

func TestAnUnenrolledOrUnconfiguredWatcherDoesNothing(t *testing.T) {
	uc := assistant.New(assistant.Dependencies{})
	done := make(chan struct{})
	go func() { uc.WatchOutbox(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a watcher without a brain must return at once")
	}
}
