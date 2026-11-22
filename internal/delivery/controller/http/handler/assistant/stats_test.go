package assistant_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"

	assistantHandler "github.com/cybericebox/backend/internal/delivery/controller/http/handler/assistant"
	"github.com/cybericebox/backend/internal/delivery/controller/http/response"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	assistantUseCase "github.com/cybericebox/backend/internal/useCase/assistant"
)

type statsPort struct {
	defaults map[string]assistantModel.StatsMode
	events   map[uuid.UUID]assistantModel.StatsMode
}

func (p *statsPort) AssistantStatsDefaults(context.Context) ([]assistantUseCase.SourceMode, error) {
	return []assistantUseCase.SourceMode{{Source: "events", Mode: p.defaults["events"]}}, nil
}

func (p *statsPort) AssistantSetStatsDefault(_ context.Context, source string, mode assistantModel.StatsMode) error {
	if source != "events" || mode == assistantModel.StatsModeDefault {
		return assistantModel.ErrAssistantStatsInvalid.Err()
	}
	p.defaults[source] = mode
	return nil
}

func (p *statsPort) AssistantEventStats(_ context.Context, e uuid.UUID) (assistantUseCase.EventStatsMode, error) {
	mode, ok := p.events[e]
	if !ok {
		return assistantUseCase.EventStatsMode{Mode: assistantModel.StatsModeDefault, Effective: p.defaults["events"]}, nil
	}
	return assistantUseCase.EventStatsMode{Mode: mode, Effective: mode}, nil
}

func (p *statsPort) AssistantSetEventStats(_ context.Context, e uuid.UUID, mode assistantModel.StatsMode) error {
	if e == uuid.Nil {
		return assistantModel.ErrAssistantStatsEventNotFound.Err()
	}
	p.events[e] = mode
	return nil
}

func statsServer(t *testing.T) (*httptest.Server, *statsPort) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	port := &statsPort{defaults: map[string]assistantModel.StatsMode{"events": assistantModel.StatsModeOff}, events: map[uuid.UUID]assistantModel.StatsMode{}}
	engine := gin.New()
	engine.Use(response.WithErrorHandler)
	assistantHandler.NewStats(port, protector{}).Init(engine.Group("api"))
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, port
}

func statsCall(t *testing.T, srv *httptest.Server, method, path, body string) (*http.Response, envelope) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	req.Header.Set("X-Test-User", author.String())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp, decodeEnvelope(t, resp)
}

func TestTheStatisticsDefaultsAreListedAndSet(t *testing.T) {
	srv, port := statsServer(t)
	resp, got := statsCall(t, srv, http.MethodGet, "/api/admin/assistant/statistics", "")
	if resp.StatusCode != 200 || !strings.Contains(string(got.Data), `"Source":"events"`) || !strings.Contains(string(got.Data), `"Mode":"off"`) {
		t.Fatalf("%d %s", resp.StatusCode, got.Data)
	}
	if resp, _ = statsCall(t, srv, http.MethodPut, "/api/admin/assistant/statistics/defaults/events", `{"Mode":"final"}`); resp.StatusCode != 200 || port.defaults["events"] != assistantModel.StatsModeFinal {
		t.Fatalf("%d %v", resp.StatusCode, port.defaults)
	}
}

func TestABadModeOrSourceIsRefusedWithTheCatalogCode(t *testing.T) {
	srv, _ := statsServer(t)
	want := assistantModel.ErrAssistantStatsInvalid.Err().StatusCode()
	for _, c := range []struct{ path, body string }{
		{"/api/admin/assistant/statistics/defaults/events", `{"Mode":"sometimes"}`},
		{"/api/admin/assistant/statistics/defaults/events", `{"Mode":"default"}`},
		{"/api/admin/assistant/statistics/defaults/academy", `{"Mode":"final"}`},
	} {
		resp, got := statsCall(t, srv, http.MethodPut, c.path, c.body)
		if resp.StatusCode != want.HTTPCode() || got.Status.Code != want.FullCode() {
			t.Errorf("%s %s: %d %+v", c.path, c.body, resp.StatusCode, got.Status)
		}
	}
	if resp, _ := statsCall(t, srv, http.MethodPut, "/api/admin/assistant/statistics/defaults/events", `{}`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a missing mode: %d", resp.StatusCode)
	}
}

func TestAnEventsModeIsSetReadAndAMissingEventIsNotFound(t *testing.T) {
	srv, _ := statsServer(t)
	event := "eeeeeeee-0000-4000-8000-000000000001"
	if resp, _ := statsCall(t, srv, http.MethodPut, "/api/admin/assistant/statistics/events/"+event, `{"Mode":"continuous"}`); resp.StatusCode != 200 {
		t.Fatalf("set: %d", resp.StatusCode)
	}
	resp, got := statsCall(t, srv, http.MethodGet, "/api/admin/assistant/statistics/events/"+event, "")
	if resp.StatusCode != 200 || !strings.Contains(string(got.Data), `"Mode":"continuous"`) || !strings.Contains(string(got.Data), `"Effective":"continuous"`) {
		t.Fatalf("%d %s", resp.StatusCode, got.Data)
	}
	missing := assistantModel.ErrAssistantStatsEventNotFound.Err().StatusCode()
	resp, got = statsCall(t, srv, http.MethodPut, "/api/admin/assistant/statistics/events/"+uuid.Nil.String(), `{"Mode":"off"}`)
	if resp.StatusCode != missing.HTTPCode() || got.Status.Code != missing.FullCode() {
		t.Fatalf("missing event: %d %+v", resp.StatusCode, got.Status)
	}
	if resp, _ = statsCall(t, srv, http.MethodGet, "/api/admin/assistant/statistics/events/not-a-uuid", ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad id: %d", resp.StatusCode)
	}
}
