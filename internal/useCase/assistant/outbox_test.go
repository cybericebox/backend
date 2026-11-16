package assistant_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/delivery/infrastructure/brain/brainfake"
	"github.com/cybericebox/backend/internal/useCase/assistant"
)

type memCursor struct {
	mu  sync.Mutex
	seq int64
}

func (c *memCursor) OutboxSeq(context.Context, string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seq, nil
}

func (c *memCursor) SetOutboxSeq(_ context.Context, _ string, seq int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if seq > c.seq {
		c.seq = seq
	}
	return nil
}

func (c *memCursor) get() int64 { c.mu.Lock(); defer c.mu.Unlock(); return c.seq }

// outboxBrain is the brain's side of the outbox: the requests it still holds, and what the platform answered.
type outboxBrain struct {
	mu          sync.Mutex
	frames      []outboxFrame
	answers     map[string][]string // request id -> bodies
	afterSeen   []string
	answerCodes []int // statuses to answer the next answers with (then 200)
	watches     int
	// archives are the task archives uploaded for a request (provide_task); archiveCodes the statuses the next uploads are answered with (then 202)
	archives     map[string][]archiveUpload
	archiveCodes []int
}

type archiveUpload struct {
	Field, Filename, ContentType, RequestedWith string
	Data                                        []byte
}

type outboxFrame struct {
	seq   int64
	event string
	data  string
}

func (b *outboxBrain) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/v1/requests/watch":
			b.mu.Lock()
			b.watches++
			b.afterSeen = append(b.afterSeen, req.URL.Query().Get("after"))
			frames := append([]outboxFrame(nil), b.frames...)
			b.mu.Unlock()
			after, _ := strconv.ParseInt(req.URL.Query().Get("after"), 10, 64)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: heartbeat\ndata: {}\n\n")
			for _, f := range frames {
				if f.seq <= after {
					continue
				}
				_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", f.seq, f.event, f.data)
			}
		case strings.HasPrefix(req.URL.Path, "/v1/requests/") && strings.HasSuffix(req.URL.Path, "/answer"):
			id := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/v1/requests/"), "/answer")
			body, _ := io.ReadAll(req.Body)
			b.mu.Lock()
			if b.answers == nil {
				b.answers = map[string][]string{}
			}
			b.answers[id] = append(b.answers[id], string(body))
			status := 0
			if len(b.answerCodes) > 0 {
				status, b.answerCodes = b.answerCodes[0], b.answerCodes[1:]
			}
			b.mu.Unlock()
			if status != 0 {
				brainfake.Fail(w, status, "x", "x")
				return
			}
			_, _ = w.Write([]byte("{}"))
		case strings.HasPrefix(req.URL.Path, "/v1/requests/") && strings.HasSuffix(req.URL.Path, "/archive") && req.Method == http.MethodPost:
			id := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/v1/requests/"), "/archive")
			upload := archiveUpload{RequestedWith: req.Header.Get("X-Requested-With")}
			if err := req.ParseMultipartForm(64 << 20); err == nil && req.MultipartForm != nil {
				for field, headers := range req.MultipartForm.File {
					for _, h := range headers {
						f, _ := h.Open()
						data, _ := io.ReadAll(f)
						_ = f.Close()
						upload.Field, upload.Filename, upload.ContentType, upload.Data = field, h.Filename, h.Header.Get("Content-Type"), data
					}
				}
			}
			b.mu.Lock()
			if b.archives == nil {
				b.archives = map[string][]archiveUpload{}
			}
			status := http.StatusAccepted
			if len(b.archiveCodes) > 0 {
				status, b.archiveCodes = b.archiveCodes[0], b.archiveCodes[1:]
			}
			if status < 400 {
				b.archives[id] = append(b.archives[id], upload)
			}
			b.mu.Unlock()
			if status >= 400 {
				brainfake.Fail(w, status, "x", "x")
				return
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte("{}"))
		default:
			brainfake.Fail(w, http.StatusNotFound, "not_found", "no route")
		}
	})
}

func (b *outboxBrain) uploaded(id string) []archiveUpload {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]archiveUpload(nil), b.archives[id]...)
}

func (b *outboxBrain) watchCount() int { b.mu.Lock(); defer b.mu.Unlock(); return b.watches }

func (b *outboxBrain) answered(id string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.answers[id]...)
}

type outboxRig struct {
	*rig
	brain  *outboxBrain
	cursor *memCursor
}

func newOutboxRig(t *testing.T, frames ...outboxFrame) *outboxRig {
	t.Helper()
	cursor := &memCursor{}
	r := newRig(t, func(d *assistant.Dependencies) {
		d.Cursor = cursor
		d.WatchMin, d.WatchMax, d.AnswerPause = 5*time.Millisecond, 20*time.Millisecond, time.Millisecond
	})
	if _, err := r.uc.BootstrapBrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := &outboxBrain{frames: frames}
	r.fake.Assistant = b.handler()
	return &outboxRig{rig: r, brain: b, cursor: cursor}
}

// run holds the outbox until stop is called (the leadership lost).
func (o *outboxRig) run(t *testing.T) (stop func()) {
	t.Helper()
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

func TestALabFromADraftAndAnyDeviceAreAnsweredNotSupportedYetAndTheCursorMoves(t *testing.T) {
	draft := outboxFrame{seq: 1, event: "provide_lab", data: `{"request_id":"r1","type":"provide_lab","subject":"` + uuid.Nil.String() + `","source":{"draft":"d-1"}}`}
	o := newOutboxRig(t, draft, req(2, "r2", "provide_device"))
	o.run(t)
	eventually(t, "both answered and the cursor moved", func() bool { return o.cursor.get() == 2 })
	for _, id := range []string{"r1", "r2"} {
		answers := o.brain.answered(id)
		if len(answers) != 1 || refusalCode(t, answers[0]) != "not_supported_yet" {
			t.Fatalf("%s: %v", id, answers)
		}
	}
}

func TestAnUnknownRequestTypeIsRefusedAndTheStreamGoesOn(t *testing.T) {
	o := newOutboxRig(t, req(1, "r1", "teleport"), outboxFrame{seq: 2, event: "x", data: "{not json"}, req(3, "r3", "provide_lab"))
	o.run(t)
	eventually(t, "the last request answered", func() bool { return o.cursor.get() == 3 })
	if a := o.brain.answered("r1"); len(a) != 1 || refusalCode(t, a[0]) != "unsupported_request" {
		t.Fatalf("unknown type: %v", a)
	}
	if a := o.brain.answered("r3"); len(a) != 1 {
		t.Fatalf("the request after the broken one was answered: %v", a)
	}
}

func TestTheStreamResumesAfterTheStoredCursor(t *testing.T) {
	o := newOutboxRig(t, req(5, "old", "provide_lab"), req(6, "new", "provide_lab"))
	o.cursor.seq = 5
	o.run(t)
	eventually(t, "the new request answered", func() bool { return o.cursor.get() == 6 })
	o.brain.mu.Lock()
	first := o.brain.afterSeen[0]
	o.brain.mu.Unlock()
	if first != "5" {
		t.Fatalf("resumed with after=%s", first)
	}
	if len(o.brain.answered("old")) != 0 {
		t.Fatal("an answered request must not be answered again after a restart")
	}
}

func TestAnAnswerIsRetriedOnServerErrorsAndSentOnce(t *testing.T) {
	o := newOutboxRig(t, req(1, "r1", "provide_lab"))
	o.brain.answerCodes = []int{http.StatusServiceUnavailable, http.StatusBadGateway}
	o.run(t)
	eventually(t, "answered", func() bool { return o.cursor.get() == 1 })
	if n := len(o.brain.answered("r1")); n != 3 {
		t.Fatalf("two failures and one success: %d attempts", n)
	}
	// The same answer every time (idempotent by request_id).
	a := o.brain.answered("r1")
	if a[0] != a[1] || a[1] != a[2] {
		t.Fatalf("attempts differ: %v", a)
	}
}

func TestAnAnswerTheBrainCallsFinalMovesTheCursor(t *testing.T) {
	for _, status := range []int{http.StatusConflict, http.StatusNotFound, http.StatusGone} {
		o := newOutboxRig(t, req(1, "r1", "provide_lab"))
		o.brain.answerCodes = []int{status}
		o.run(t)
		eventually(t, fmt.Sprintf("cursor after %d", status), func() bool { return o.cursor.get() == 1 })
		if n := len(o.brain.answered("r1")); n != 1 {
			t.Fatalf("%d: %d attempts", status, n)
		}
	}
}

func TestWhenAnswersKeepFailingTheCursorStaysAndTheRequestIsSentAgain(t *testing.T) {
	o := newOutboxRig(t, req(1, "r1", "provide_lab"), req(2, "r2", "provide_lab"))
	// More failures than one connection retries: the stream is dropped and the brain sends it again.
	o.brain.answerCodes = []int{503, 503, 503, 503, 503}
	o.run(t)
	eventually(t, "both answered after the reconnect", func() bool { return o.cursor.get() == 2 })
	o.brain.mu.Lock()
	watches := o.brain.watches
	o.brain.mu.Unlock()
	if watches < 2 {
		t.Fatalf("the stream must have been reopened: %d", watches)
	}
	if len(o.brain.answered("r1")) < 6 {
		t.Fatalf("r1 answered %d times", len(o.brain.answered("r1")))
	}
}

func TestTheWatcherStopsWhenLeadershipIsLost(t *testing.T) {
	o := newOutboxRig(t)
	stop := o.run(t)
	eventually(t, "the first connection", func() bool {
		o.brain.mu.Lock()
		defer o.brain.mu.Unlock()
		return o.brain.watches >= 1
	})
	stop() // fails the test itself if the watcher does not return
	o.brain.mu.Lock()
	seen := o.brain.watches
	o.brain.mu.Unlock()
	time.Sleep(60 * time.Millisecond)
	o.brain.mu.Lock()
	defer o.brain.mu.Unlock()
	if o.brain.watches != seen {
		t.Fatal("the watcher reconnected after it was stopped")
	}
}

func TestTheWatcherReconnectsAfterTheBrainDropsTheStream(t *testing.T) {
	o := newOutboxRig(t)
	o.run(t)
	eventually(t, "several connections", func() bool {
		o.brain.mu.Lock()
		defer o.brain.mu.Unlock()
		return o.brain.watches >= 3
	})
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

func TestABrokenRequestWithAReadableIDIsAnsweredInvalidBeforeTheCursorMoves(t *testing.T) {
	o := newOutboxRig(t,
		outboxFrame{seq: 1, event: "x", data: `{"request_id":"r1","type":"provide_data","task_refs":[`},   // cut off JSON
		outboxFrame{seq: 2, event: "x", data: `{"request_id":"r2","type":["not","a","string"]}`},          // valid JSON, wrong shape
		outboxFrame{seq: 3, event: "x", data: `no id at all {`},                                           // nothing to answer to
		outboxFrame{seq: 4, event: "x", data: `{"request_id":"bad/id with spaces","type":"provide_lab"}`}, // unusable id
		req(5, "r5", "provide_lab"),
	)
	o.run(t)
	eventually(t, "all handled", func() bool { return o.cursor.get() == 5 })
	for _, id := range []string{"r1", "r2"} {
		a := o.brain.answered(id)
		if len(a) != 1 || refusalCode(t, a[0]) != "invalid_request" {
			t.Fatalf("%s: %v", id, a)
		}
	}
	o.brain.mu.Lock()
	defer o.brain.mu.Unlock()
	if len(o.brain.answers) != 3 {
		t.Fatalf("only requests with a readable id are answered (r1, r2, r5): %v", o.brain.answers)
	}
}

func TestAnInvalidAnswerIsNotAdvancedPastWhenTheBrainKeepsFailing(t *testing.T) {
	o := newOutboxRig(t, outboxFrame{seq: 1, event: "x", data: `{"request_id":"r1","type":`})
	o.brain.answerCodes = []int{503, 503, 503, 503, 503}
	o.run(t)
	eventually(t, "answered after the reconnect", func() bool { return o.cursor.get() == 1 })
	if len(o.brain.answered("r1")) < 6 {
		t.Fatalf("answers: %d", len(o.brain.answered("r1")))
	}
}
