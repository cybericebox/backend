package assistant_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"

	assistantHandler "github.com/cybericebox/daemon/internal/delivery/controller/http/handler/assistant"
	"github.com/cybericebox/daemon/internal/delivery/controller/http/response"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
	assistantUseCase "github.com/cybericebox/daemon/internal/useCase/assistant"
	exerciseUseCase "github.com/cybericebox/daemon/internal/useCase/exercise"
)

type measurePort struct {
	mu      sync.Mutex
	err     error
	run     assistantModel.Run
	events  []assistantModel.RunEvent
	state   assistantModel.RunState
	started int
	stopped int
	gotUser uuid.UUID
}

func (p *measurePort) StartGraderRun(_ context.Context, a exerciseUseCase.Actor, _, version, variant uuid.UUID) (assistantModel.Run, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.started++
	p.gotUser = a.UserID
	if p.err != nil {
		return assistantModel.Run{}, p.err
	}
	p.run = assistantModel.Run{ID: uuid.Must(uuid.NewV7()), VersionID: version, VariantID: variant, DeployID: uuid.Must(uuid.NewV7()), State: assistantModel.RunStarting, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	return p.run, nil
}
func (p *measurePort) GraderRuns(context.Context, exerciseUseCase.Actor, uuid.UUID, uuid.UUID, uuid.UUID) ([]assistantModel.Run, error) {
	return []assistantModel.Run{p.run}, p.err
}
func (p *measurePort) GraderRun(context.Context, exerciseUseCase.Actor, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) (assistantUseCase.RunView, error) {
	if p.err != nil {
		return assistantUseCase.RunView{}, p.err
	}
	return assistantUseCase.RunView{Run: p.run, Answers: []assistantModel.RunAnswer{{Seq: 3, SubtaskRef: "login", Correct: true, At: time.Now()}}}, nil
}
func (p *measurePort) StopGraderRun(context.Context, exerciseUseCase.Actor, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) (assistantModel.Run, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped++
	run := p.run
	run.State = assistantModel.RunClosing
	return run, p.err
}
func (p *measurePort) GraderRunEvents(context.Context, exerciseUseCase.Actor, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) (func(context.Context, int64, int) ([]assistantModel.RunEvent, assistantModel.RunState, error), error) {
	if p.err != nil {
		return nil, p.err
	}
	return func(_ context.Context, after int64, _ int) ([]assistantModel.RunEvent, assistantModel.RunState, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		var out []assistantModel.RunEvent
		for _, e := range p.events {
			if e.Seq > after {
				out = append(out, e)
			}
		}
		return out, p.state, nil
	}, nil
}

func measureServer(t *testing.T) (*httptest.Server, *measurePort) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	port := &measurePort{state: assistantModel.RunRunning}
	engine := gin.New()
	engine.Use(response.WithErrorHandler)
	assistantHandler.NewMeasure(port, protector{}).Init(engine.Group("api"))
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, port
}

var measureBase = "/api/exercises/" + uuid.Must(uuid.NewV7()).String() + "/versions/" + uuid.Must(uuid.NewV7()).String() + "/variants/" + uuid.Must(uuid.NewV7()).String() + "/measure"

func measureCall(t *testing.T, srv *httptest.Server, method, path string, header ...string) (*http.Response, envelope) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, nil)
	req.Header.Set("X-Test-User", author.String())
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp, decodeEnvelope(t, resp)
}

func TestStartAnswers202WithTheRunAndNothingSecret(t *testing.T) {
	srv, port := measureServer(t)
	resp, got := measureCall(t, srv, http.MethodPost, measureBase)
	if resp.StatusCode != http.StatusAccepted || port.started != 1 || port.gotUser != author {
		t.Fatalf("%d %+v", resp.StatusCode, port)
	}
	body := string(got.Data)
	if !strings.Contains(body, `"State":"starting"`) || strings.Contains(body, "AccessClient") || strings.Contains(strings.ToLower(body), "wireguard") {
		t.Fatalf("%s", body)
	}
}

func TestEveryRouteAnswersTheUseCaseError(t *testing.T) {
	srv, port := measureServer(t)
	port.err = assistantModel.ErrAssistantRunActive.Err()
	want := assistantModel.ErrAssistantRunActive.Err().StatusCode()
	run := uuid.Must(uuid.NewV7()).String()
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, measureBase}, {http.MethodGet, measureBase}, {http.MethodGet, measureBase + "/" + run},
		{http.MethodPost, measureBase + "/" + run + "/stop"}, {http.MethodGet, measureBase + "/" + run + "/events"},
	} {
		resp, got := measureCall(t, srv, c.method, c.path)
		if resp.StatusCode != want.HTTPCode() || got.Status.Code != want.FullCode() {
			t.Errorf("%s %s: %d %+v", c.method, c.path, resp.StatusCode, got.Status)
		}
	}
	// a bad id is a bad request, not a call into the use case
	if resp, _ := measureCall(t, srv, http.MethodGet, measureBase+"/not-a-uuid"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("%d", resp.StatusCode)
	}
	if resp, _ := measureCall(t, srv, http.MethodGet, measureBase+"/"+run+"/events?after=x"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a bad position: %d", resp.StatusCode)
	}
	// no session: 401 before anything
	req, _ := http.NewRequest(http.MethodPost, srv.URL+measureBase, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("%d", resp.StatusCode)
	}
}

func TestGetAndStop(t *testing.T) {
	srv, port := measureServer(t)
	port.run = assistantModel.Run{ID: uuid.Must(uuid.NewV7()), State: assistantModel.RunRunning, Measurement: []byte(`{"steps":7}`), CreatedAt: time.Now(), ExpiresAt: time.Now()}
	resp, got := measureCall(t, srv, http.MethodGet, measureBase+"/"+port.run.ID.String())
	if resp.StatusCode != 200 || !strings.Contains(string(got.Data), `"Measurement":{"steps":7}`) || !strings.Contains(string(got.Data), `"SubtaskRef":"login"`) || !strings.Contains(string(got.Data), `"Correct":true`) {
		t.Fatalf("%d %s", resp.StatusCode, got.Data)
	}
	resp, got = measureCall(t, srv, http.MethodPost, measureBase+"/"+port.run.ID.String()+"/stop")
	if resp.StatusCode != 200 || port.stopped != 1 || !strings.Contains(string(got.Data), `"State":"closing"`) {
		t.Fatalf("%d %s", resp.StatusCode, got.Data)
	}
	resp, got = measureCall(t, srv, http.MethodGet, measureBase)
	if resp.StatusCode != 200 || !strings.HasPrefix(string(got.Data), "[") {
		t.Fatalf("%d %s", resp.StatusCode, got.Data)
	}
}

func readFramesUntilClosed(t *testing.T, resp *http.Response) []string {
	t.Helper()
	var frames []string
	var current []string
	sc := bufio.NewScanner(resp.Body)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				if len(current) > 0 {
					frames = append(frames, strings.Join(current, "|"))
				}
				current = nil
				continue
			}
			current = append(current, line)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream of a finished run must end")
	}
	return frames
}

func TestTheEventStreamIsServedFromStorageAndEndsWithAFinishedRun(t *testing.T) {
	srv, port := measureServer(t)
	port.state = assistantModel.RunFinished
	port.events = []assistantModel.RunEvent{
		{Seq: 1, Type: "status", Data: `{"type":"status"}`}, {Seq: 2, Type: "answer", Data: `{"type":"answer","value":"x"}`}, {Seq: 3, Type: "run_finished", Data: `{"type":"run_finished","status":"done"}`},
	}
	run := uuid.Must(uuid.NewV7()).String()
	resp, _ := measureCallStream(t, srv, measureBase+"/"+run+"/events")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("%d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	frames := readFramesUntilClosed(t, resp)
	if len(frames) < 3 || frames[0] != `id: 1|event: status|data: {"type":"status"}` || !strings.HasPrefix(frames[2], "id: 3|event: run_finished|") {
		t.Fatalf("%q", frames)
	}

	// resumed after a position: only what follows (query and Last-Event-ID)
	for _, c := range []struct{ path, header string }{{"/events?after=2", ""}, {"/events", "2"}} {
		var hdr []string
		if c.header != "" {
			hdr = []string{"Last-Event-ID", c.header}
		}
		resp, _ = measureCallStream(t, srv, measureBase+"/"+run+c.path, hdr...)
		frames = readFramesUntilClosed(t, resp)
		if len(frames) != 1 || !strings.HasPrefix(frames[0], "id: 3|") {
			t.Fatalf("%s: %q", c.path, frames)
		}
	}
}

func measureCallStream(t *testing.T, srv *httptest.Server, path string, header ...string) (*http.Response, error) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	req.Header.Set("X-Test-User", author.String())
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp, err
}

func TestAStreamOfARunThatIsStillGoingKeepsOpenAndPicksUpNewEvents(t *testing.T) {
	srv, port := measureServer(t)
	run := uuid.Must(uuid.NewV7()).String()
	resp, _ := measureCallStream(t, srv, measureBase+"/"+run+"/events")
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	time.Sleep(100 * time.Millisecond)
	port.mu.Lock()
	port.events = append(port.events, assistantModel.RunEvent{Seq: 1, Type: "step", Data: `{"type":"step"}`})
	port.mu.Unlock()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case line, open := <-lines:
			if !open {
				t.Fatal("a running run's stream must stay open")
			}
			if line == `data: {"type":"step"}` {
				return
			}
		case <-deadline:
			t.Fatal("the new event never arrived")
		}
	}
}
