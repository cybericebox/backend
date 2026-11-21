package auth_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	authHandler "github.com/cybericebox/daemon/internal/delivery/controller/http/handler/auth"
)

// The session keeps the connection's peer address only when it is public; a forwarding header never counts, and a
// private, CGNAT, loopback or link-local peer is dropped.
func TestSignIn_SessionIPIsThePublicPeerNeverAHeader(t *testing.T) {
	cases := []struct {
		name, peer, xff, wantIP string
	}{
		{"a pod peer is dropped, the header is ignored", "10.64.3.2:5000", "203.0.113.9", ""},
		{"a public peer is the client, a spoofed header is ignored", "198.51.100.4:5000", "203.0.113.9", "198.51.100.4"},
		{"a public IPv6 peer", "[2001:4860:4860::8888]:5000", "", "2001:4860:4860::8888"},
		{"a CGNAT peer is dropped", "100.64.2.3:5000", "203.0.113.9", ""},
		{"a link-local IPv6 peer is dropped", "[fe80::1]:5000", "", ""},
		{"loopback peer", "127.0.0.1:5000", "", ""},
		{"IPv6 loopback peer", "[::1]:5000", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			r := gin.New()
			if err := r.SetTrustedProxies(nil); err != nil {
				t.Fatal(err)
			}
			uc := &fakeUC{cookie: "c", redirect: "l"}
			h := authHandler.NewAuthAPIHandler(uc, &fakeProt{}, testAuthConfig)
			api := r.Group("api")
			h.Init(api, api)

			req := httptest.NewRequest(
				http.MethodPost,
				"/api/auth/sign-in",
				strings.NewReader(`{"Email":"a@b.test","Password":"Secret!1"}`),
			)
			req.RemoteAddr = c.peer
			if c.xff != "" {
				req.Header.Set("X-Forwarded-For", c.xff)
			}
			req.Header.Set("User-Agent", "ua")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", w.Code)
			}
			if uc.capturedMeta.IP != c.wantIP || uc.capturedMeta.UserAgent != "ua" {
				t.Fatalf("session metadata = %+v, want IP %q", uc.capturedMeta, c.wantIP)
			}
		})
	}
}
