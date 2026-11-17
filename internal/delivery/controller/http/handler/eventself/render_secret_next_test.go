package eventself

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"

	"github.com/cybericebox/daemon/internal/config"
)

const testRenderSecretNext = "fedcba9876543210fedcba9876543210-next-render-secret"

// renderStatus asks the key route (it needs only the render secret) with one header value.
func renderStatus(t *testing.T, cfg config.AuthConfig, header *string) int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewEventRenderAPIHandler(&renderFake{id: uuid.Must(uuid.NewV4())}, renderGate{}, cfg).Init(r.Group("api"))
	req := httptest.NewRequest(http.MethodGet, "/api/render/key", nil)
	if header != nil {
		req.Header.Set(renderSecretHeader, *header)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Code
}

func renderConfig(next string) config.AuthConfig {
	return config.AuthConfig{
		SessionEncryptionKey: "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
		RenderSecret:         testRenderSecret, RenderSecretNext: next,
		Hosts: config.HostsConfig{Domain: "example.test", EventDomain: "example.test"},
	}
}

func TestRenderSecretNextIsAcceptedNextToTheCurrentValue(t *testing.T) {
	current, next, other := testRenderSecret, testRenderSecretNext, "another-value-that-is-not-a-render-secret"
	cfg := renderConfig(testRenderSecretNext)
	require.Equal(t, http.StatusOK, renderStatus(t, cfg, &current), "the current value still works")
	require.Equal(t, http.StatusOK, renderStatus(t, cfg, &next), "the next value works")
	require.Equal(t, http.StatusNotFound, renderStatus(t, cfg, &other), "any other value is a 404, like before")
	require.Equal(t, http.StatusNotFound, renderStatus(t, cfg, nil), "no header is a 404")
}

func TestRenderSecretNextEmptyIsOff(t *testing.T) {
	current, empty := testRenderSecret, ""
	cfg := renderConfig("")
	require.Equal(t, http.StatusOK, renderStatus(t, cfg, &current))
	require.Equal(t, http.StatusNotFound, renderStatus(t, cfg, &empty), "an empty header must never match an unset next value")
}

func TestRenderSecretNextAloneDoesNotOpenAnUnsetCurrent(t *testing.T) {
	next := testRenderSecretNext
	cfg := renderConfig(testRenderSecretNext)
	cfg.RenderSecret = ""
	require.Equal(t, http.StatusOK, renderStatus(t, cfg, &next), "after the move the next value is the only one set")
}
