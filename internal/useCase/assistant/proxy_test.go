package assistant_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cybericebox/backend/internal/delivery/infrastructure/brain/brainfake"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	"github.com/cybericebox/backend/internal/useCase/assistant"
)

func enrolledRig(t *testing.T, mutate func(*assistant.Dependencies)) *rig {
	t.Helper()
	r := newRig(t, mutate)
	if _, err := r.uc.BootstrapBrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestProxyForwardsTheCallWithTheSubjectAndNeverTheCallersOwn(t *testing.T) {
	r := enrolledRig(t, nil)
	var got http.Header
	var gotPath, gotQuery, gotBody string
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got, gotPath, gotQuery = req.Header.Clone(), req.URL.Path, req.URL.RawQuery
		b, _ := io.ReadAll(req.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ID":"s1"}`))
	})

	body := `{"Text":"hello"}`
	resp, err := r.uc.Proxy(context.Background(), userOne, assistant.Call{
		Method: http.MethodPost, Path: "/v1/assistant/sessions/s1/messages",
		Header: http.Header{"Content-Type": {"application/json"}, assistant.SubjectHeader: {"spoofed"}},
		Body:   strings.NewReader(body), ContentLength: int64(len(body)),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if gotPath != "/v1/assistant/sessions/s1/messages" || gotBody != body || string(out) != `{"ID":"s1"}` {
		t.Fatalf("path=%q body=%q out=%q", gotPath, gotBody, out)
	}
	if subject := got.Get(assistant.SubjectHeader); subject != subjectOf(t, r.uc, userOne) || subject == "spoofed" || strings.Contains(subject, userOne.String()) {
		t.Fatalf("subject header = %q", subject)
	}
	if got.Get("Content-Type") != "application/json" {
		t.Fatalf("content type = %q", got.Get("Content-Type"))
	}
	_ = gotQuery
}

func TestProxyMapsBrainFailuresByStatusToTypedErrors(t *testing.T) {
	r := enrolledRig(t, nil)
	status := 0
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		brainfake.Fail(w, status, "some_code", "free text that must never decide anything")
	})
	cases := map[int]error{
		http.StatusBadRequest:            assistantModel.ErrAssistantRejected.Err(),
		http.StatusUnsupportedMediaType:  assistantModel.ErrAssistantRejected.Err(),
		http.StatusUnprocessableEntity:   assistantModel.ErrAssistantRejected.Err(),
		http.StatusForbidden:             assistantModel.ErrAssistantForbidden.Err(),
		http.StatusNotFound:              assistantModel.ErrAssistantNotFound.Err(),
		http.StatusConflict:              assistantModel.ErrAssistantNotReady.Err(),
		http.StatusGone:                  assistantModel.ErrAssistantGone.Err(),
		http.StatusUnauthorized:          assistantModel.ErrAssistantUpstream.Err(),
		http.StatusInternalServerError:   assistantModel.ErrAssistantUpstream.Err(),
		http.StatusBadGateway:            assistantModel.ErrAssistantUpstream.Err(),
		http.StatusServiceUnavailable:    assistantModel.ErrAssistantUpstream.Err(),
		http.StatusTeapot:                assistantModel.ErrAssistantUpstream.Err(),
		http.StatusRequestEntityTooLarge: assistantModel.ErrAssistantUpstream.Err(),
	}
	for st, want := range cases {
		status = st
		_, err := r.uc.Proxy(context.Background(), userOne, assistant.Call{Method: http.MethodGet, Path: "/v1/assistant/ideas"})
		if !errors.Is(err, want) {
			t.Errorf("status %d: got %v, want %v", st, err, want)
		}
	}
}

func TestProxyUnreachableBrainIsATransient503Error(t *testing.T) {
	r := enrolledRig(t, nil)
	r.fake.Server.Close()
	_, err := r.uc.Proxy(context.Background(), userOne, assistant.Call{Method: http.MethodGet, Path: "/v1/assistant/ideas"})
	if !errors.Is(err, assistantModel.ErrAssistantBrainUnreachable.Err()) {
		t.Fatalf("closed brain: %v", err)
	}
}

func TestProxyBeforeEnrollmentIsUnavailable(t *testing.T) {
	r := newRig(t, nil) // configured, never enrolled
	if !r.uc.Enabled() {
		t.Fatal("configured")
	}
	_, err := r.uc.Proxy(context.Background(), userOne, assistant.Call{Method: http.MethodGet, Path: "/v1/assistant/ideas"})
	if !errors.Is(err, assistantModel.ErrAssistantUnavailable.Err()) {
		t.Fatalf("not enrolled: %v", err)
	}
}
