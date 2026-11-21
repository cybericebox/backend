package http

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cybericebox/daemon/internal/config"
	"github.com/cybericebox/daemon/internal/delivery/controller/http/middleware"
)

func hardened(t *testing.T, cfg config.HTTPControllerConfig) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := hardenRouter(r, &cfg); err != nil {
		t.Fatal(err)
	}
	r.GET("/ip", func(c *gin.Context) { c.String(http.StatusOK, c.ClientIP()) })
	r.POST("/json", func(c *gin.Context) {
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.Status(http.StatusOK)
	})
	r.POST("/upload", func(c *gin.Context) {
		middleware.LimitBody(c, 4096)
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.Status(http.StatusOK)
	})
	return r
}

func send(r *gin.Engine, method, path string, body []byte, remote, xff string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.RemoteAddr = remote + ":5555"
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// A client picks its own address with a forwarding header to dodge the per-address limits: no header counts,
// whoever the peer is.
func TestNoForwardingHeaderChoosesTheClientAddress(t *testing.T) {
	r := hardened(t, config.HTTPControllerConfig{MaxBodyBytes: 1 << 20})
	for _, peer := range []string{"203.0.113.50", "10.1.2.3"} {
		if got := send(r, http.MethodGet, "/ip", nil, peer, "198.51.100.7").Body.String(); got != peer {
			t.Fatalf("peer %s: the connection address stands, got %s", peer, got)
		}
		if got := send(r, http.MethodGet, "/ip", nil, peer, "1.2.3.4, 198.51.100.7").Body.String(); got != peer {
			t.Fatalf("peer %s: a forged chain: %s", peer, got)
		}
	}
}

func TestEveryBodyIsCappedAndAnUploadRaisesItsOwnCap(t *testing.T) {
	r := hardened(t, config.HTTPControllerConfig{MaxBodyBytes: 1024})
	if w := send(r, http.MethodPost, "/json", bytes.Repeat([]byte("a"), 800), "203.0.113.1", ""); w.Code != http.StatusOK {
		t.Fatalf("a small body: %d", w.Code)
	}
	if w := send(r, http.MethodPost, "/json", bytes.Repeat([]byte("a"), 2000), "203.0.113.1", ""); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a body over the cap: %d", w.Code)
	}
	if w := send(r, http.MethodPost, "/upload", bytes.Repeat([]byte("a"), 3000), "203.0.113.1", ""); w.Code != http.StatusOK {
		t.Fatalf("an upload route states a larger cap: %d", w.Code)
	}
	if w := send(r, http.MethodPost, "/upload", bytes.Repeat([]byte("a"), 5000), "203.0.113.1", ""); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("and keeps it: %d", w.Code)
	}
}

func TestEveryResponseCarriesTheSecurityHeaders(t *testing.T) {
	r := hardened(t, config.HTTPControllerConfig{MaxBodyBytes: 1024})
	w := send(r, http.MethodGet, "/ip", nil, "203.0.113.1", "")
	if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("headers = %v", w.Header())
	}
	if !strings.Contains(w.Body.String(), "203.0.113.1") {
		t.Fatal("sanity")
	}
}
