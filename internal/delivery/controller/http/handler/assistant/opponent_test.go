package assistant_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"

	assistantHandler "github.com/cybericebox/backend/internal/delivery/controller/http/handler/assistant"
	"github.com/cybericebox/backend/internal/delivery/controller/http/response"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	assistantUseCase "github.com/cybericebox/backend/internal/useCase/assistant"
	exerciseUseCase "github.com/cybericebox/backend/internal/useCase/exercise"
)

type opponentPort struct {
	mu      sync.Mutex
	err     error
	run     assistantModel.Run
	events  []assistantModel.RunEvent
	state   assistantModel.RunState
	started int
	stopped int
	index   int
	indexes []int
	gotUser uuid.UUID
}

func (p *opponentPort) StartOpponentRuns(_ context.Context, a exerciseUseCase.Actor, _, version, variant uuid.UUID, indexes []int) ([]assistantModel.Run, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.started++
	p.gotUser, p.indexes = a.UserID, indexes
	if p.err != nil {
		return nil, p.err
	}
	deploy := uuid.Must(uuid.NewV7())
	out := make([]assistantModel.Run, 0, len(indexes))
	for i, index := range indexes {
		out = append(out, assistantModel.Run{ID: uuid.Must(uuid.NewV7()), VersionID: version, VariantID: variant, DeployID: deploy, Role: "attack", Opponent: index, ToolsIndex: i, State: assistantModel.RunStarting, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)})
	}
	return out, nil
}

func (p *opponentPort) StartOpponentRun(_ context.Context, a exerciseUseCase.Actor, _, version, variant uuid.UUID, index int) (assistantModel.Run, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.started++
	p.gotUser, p.index = a.UserID, index
	if p.err != nil {
		return assistantModel.Run{}, p.err
	}
	p.run = assistantModel.Run{ID: uuid.Must(uuid.NewV7()), VersionID: version, VariantID: variant, DeployID: uuid.Must(uuid.NewV7()), Role: "attack", Opponent: index, State: assistantModel.RunStarting, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	return p.run, nil
}
func (p *opponentPort) OpponentRuns(_ context.Context, _ exerciseUseCase.Actor, _, _, _ uuid.UUID, index int) ([]assistantModel.Run, error) {
	p.index = index
	return []assistantModel.Run{p.run}, p.err
}
func (p *opponentPort) OpponentRun(_ context.Context, _ exerciseUseCase.Actor, _, _, _ uuid.UUID, index int, _ uuid.UUID) (assistantUseCase.RunView, error) {
	p.index = index
	return assistantUseCase.RunView{Run: p.run}, p.err
}
func (p *opponentPort) StopOpponentRun(_ context.Context, _ exerciseUseCase.Actor, _, _, _ uuid.UUID, index int, _ uuid.UUID) (assistantModel.Run, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped++
	p.index = index
	run := p.run
	run.State = assistantModel.RunClosing
	return run, p.err
}
func (p *opponentPort) OpponentRunEvents(_ context.Context, _ exerciseUseCase.Actor, _, _, _ uuid.UUID, index int, _ uuid.UUID) (func(context.Context, int64, int) ([]assistantModel.RunEvent, assistantModel.RunState, error), error) {
	if p.err != nil {
		return nil, p.err
	}
	p.index = index
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

func opponentServer(t *testing.T) (*httptest.Server, *opponentPort) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	port := &opponentPort{state: assistantModel.RunFinished}
	engine := gin.New()
	engine.Use(response.WithErrorHandler)
	assistantHandler.NewOpponent(port, protector{}).Init(engine.Group("api"))
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, port
}

var opponentBase = "/api/exercises/" + uuid.Must(uuid.NewV7()).String() + "/versions/" + uuid.Must(uuid.NewV7()).String() + "/variants/" + uuid.Must(uuid.NewV7()).String() + "/opponents/2/try"

func TestTryStartsTheOpponentAtThePathPositionAndNothingIsSecret(t *testing.T) {
	srv, port := opponentServer(t)
	resp, got := measureCall(t, srv, http.MethodPost, opponentBase)
	if resp.StatusCode != http.StatusAccepted || port.started != 1 || port.gotUser != author || port.index != 2 {
		t.Fatalf("%d %+v", resp.StatusCode, port)
	}
	body := string(got.Data)
	if !strings.Contains(body, `"State":"starting"`) || !strings.Contains(body, `"Opponent":2`) || !strings.Contains(body, `"Role":"attack"`) ||
		strings.Contains(body, "AccessClient") || strings.Contains(strings.ToLower(body), "token") || strings.Contains(strings.ToLower(body), "wireguard") {
		t.Fatalf("%s", body)
	}
}

func TestEveryOpponentRouteAnswersTheUseCaseErrorAndRejectsBadIds(t *testing.T) {
	srv, port := opponentServer(t)
	port.err = assistantModel.ErrAssistantOpponentNotFound.Err()
	want := assistantModel.ErrAssistantOpponentNotFound.Err().StatusCode()
	run := uuid.Must(uuid.NewV7()).String()
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, opponentBase}, {http.MethodGet, opponentBase}, {http.MethodGet, opponentBase + "/" + run},
		{http.MethodPost, opponentBase + "/" + run + "/stop"}, {http.MethodGet, opponentBase + "/" + run + "/events"},
	} {
		resp, got := measureCall(t, srv, c.method, c.path)
		if resp.StatusCode != want.HTTPCode() || got.Status.Code != want.FullCode() {
			t.Errorf("%s %s: %d %+v", c.method, c.path, resp.StatusCode, got.Status)
		}
	}
	for _, bad := range []string{"0", "-1", "x"} {
		if resp, _ := measureCall(t, srv, http.MethodPost, strings.Replace(opponentBase, "/opponents/2/", "/opponents/"+bad+"/", 1)); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("index %q: %d", bad, resp.StatusCode)
		}
	}
	if resp, _ := measureCall(t, srv, http.MethodGet, opponentBase+"/not-a-uuid"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("%d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+opponentBase, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no session: %d", resp.StatusCode)
	}
}

func TestOpponentGetStopAndEventsCarryThePathPosition(t *testing.T) {
	srv, port := opponentServer(t)
	port.run = assistantModel.Run{ID: uuid.Must(uuid.NewV7()), Role: "defense", Opponent: 2, State: assistantModel.RunRunning, Measurement: []byte(`{"steps":7}`), CreatedAt: time.Now(), ExpiresAt: time.Now()}
	port.events = []assistantModel.RunEvent{{Seq: 1, Type: "status", Data: `{"status":"working"}`}, {Seq: 2, Type: "run_finished", Data: `{"status":"done"}`}}
	id := port.run.ID.String()
	if resp, got := measureCall(t, srv, http.MethodGet, opponentBase+"/"+id); resp.StatusCode != 200 || port.index != 2 || !strings.Contains(string(got.Data), `"Measurement":{"steps":7}`) {
		t.Fatalf("%d %s", resp.StatusCode, got.Data)
	}
	if resp, got := measureCall(t, srv, http.MethodPost, opponentBase+"/"+id+"/stop"); resp.StatusCode != 200 || port.stopped != 1 || !strings.Contains(string(got.Data), `"State":"closing"`) {
		t.Fatalf("%d %s", resp.StatusCode, got.Data)
	}
	if resp, got := measureCall(t, srv, http.MethodGet, opponentBase); resp.StatusCode != 200 || !strings.HasPrefix(string(got.Data), "[") {
		t.Fatalf("%d %s", resp.StatusCode, got.Data)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+opponentBase+"/"+id+"/events?after=1", nil)
	req.Header.Set("X-Test-User", author.String())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	frames := readFramesUntilClosed(t, resp)
	if len(frames) != 1 || !strings.Contains(frames[0], "id: 2") || !strings.Contains(frames[0], "event: run_finished") {
		t.Fatalf("%v", frames)
	}
}

var opponentsBase = strings.TrimSuffix(opponentBase, "/opponents/2/try") + "/opponents/try"

// postJSON posts a body as the author.
func postJSON(t *testing.T, srv *httptest.Server, path, body string, user bool) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if user {
		req.Header.Set("X-Test-User", author.String())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestTryingSeveralOpponentsStartsThemTogetherInTheOrderOfTheBody(t *testing.T) {
	srv, port := opponentServer(t)
	resp := postJSON(t, srv, opponentsBase, `{"indexes":[3,1]}`, true)
	var got struct {
		Data []struct {
			ID       string
			Opponent int
			DeployID string
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted || port.started != 1 || port.gotUser != author || len(port.indexes) != 2 || port.indexes[0] != 3 || port.indexes[1] != 1 {
		t.Fatalf("%d %+v", resp.StatusCode, port)
	}
	if len(got.Data) != 2 || got.Data[0].Opponent != 3 || got.Data[1].Opponent != 1 || got.Data[0].DeployID != got.Data[1].DeployID || got.Data[0].ID == got.Data[1].ID {
		t.Fatalf("%+v", got)
	}
	// the single opponent's route still answers under its own path
	if resp, _ := measureCall(t, srv, http.MethodPost, opponentBase); resp.StatusCode != http.StatusAccepted || port.index != 2 {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestTryingSeveralOpponentsRefusesABadBodyAndAnswersTheUseCaseError(t *testing.T) {
	srv, port := opponentServer(t)
	for _, body := range []string{``, `{}`, `{"indexes":[]}`, `{"indexes":[0]}`, `{"indexes":[1,1]}`, `{"indexes":[1,2,3,4,5,6,7,8,9]}`, `{"indexes":"1"}`, `[1]`} {
		if resp := postJSON(t, srv, opponentsBase, body, true); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%q: %d", body, resp.StatusCode)
		}
	}
	if port.started != 0 {
		t.Fatal("nothing is started for a bad body")
	}
	port.err = assistantModel.ErrAssistantRunLimit.Err()
	want := assistantModel.ErrAssistantRunLimit.Err().StatusCode()
	if resp := postJSON(t, srv, opponentsBase, `{"indexes":[1,2]}`, true); resp.StatusCode != want.HTTPCode() {
		t.Errorf("%d", resp.StatusCode)
	}
	if resp := postJSON(t, srv, opponentsBase, `{"indexes":[1,2]}`, false); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no session: %d", resp.StatusCode)
	}
}
