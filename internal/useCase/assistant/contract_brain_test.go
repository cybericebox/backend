package assistant_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The contract test runs the REAL cyber-brain (built from the sibling checkout, no Docker, no NATS) with a fake
// OpenAI-compatible model. It checks what still goes over HTTP: the enrollment and the renewal. The control plane is gRPC
// (pkg/brainapi/v1) and is tested against the gRPC fake until the brain serves it. It is skipped when the checkout is absent (the brain is
// a private repository), or with -short. Set BRAIN_CONTRACT_DIR to point at another checkout.
func brainCheckout(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("contract test against the real brain is skipped with -short")
	}
	dir := os.Getenv("BRAIN_CONTRACT_DIR")
	if dir == "" {
		wd, _ := os.Getwd()
		for up := wd; up != filepath.Dir(up); up = filepath.Dir(up) {
			if _, err := os.Stat(filepath.Join(up, "cyber-brain", "cmd", "brain", "main.go")); err == nil {
				dir = filepath.Join(up, "cyber-brain")
				break
			}
		}
	}
	if dir == "" {
		t.Skip("no cyber-brain checkout next to the daemon")
	}
	return dir
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

// modelServer is an OpenAI-compatible endpoint whose every reply is one step of the agent loop.
func modelServer(t *testing.T, step string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "m", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": step}, "finish_reason": "stop"}},
			"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// startBrainArgs builds and starts the real brain on loopback with the measuring seat on the fake model and more flags of the
// brain; it returns the address the brain listens on.
func startBrainArgs(t *testing.T, dir, modelURL string, extra ...string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "brain")
	build := exec.Command("go", "build", "-o", bin, "./cmd/brain")
	build.Dir, build.Env = dir, append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("the brain does not build here: %v\n%s", err, out)
	}
	addr := freeAddr(t)
	if len(extra) > 0 {
		// The brain's own CA puts the host of -addr into its server certificate as a DNS name only, so an IP literal would
		// never verify: the mutual-TLS mode is reached by the name localhost.
		addr = strings.Replace(addr, "127.0.0.1", "localhost", 1)
	}
	cmd := exec.Command(bin, append([]string{"-addr", addr, "-measure-seat", "solver", "-measure-actions", "5", "-measure-runs", "1"}, extra...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "INFERENCE_BASE_URL="+modelURL, "INFERENCE_MODEL=m", "INFERENCE_PROTOCOL=openai", "INFERENCE_API_KEY=x", "BRAIN_TOKEN=")
	var logs strings.Builder
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the brain did not start: %s", logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	return addr
}
