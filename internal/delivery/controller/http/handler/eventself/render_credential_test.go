package eventself

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cybericebox/backend/internal/authz"
	"github.com/cybericebox/backend/internal/delivery/controller/http/middleware"
)

// The render secret layer marks the service credential, so the enforcement can refuse a service route whose secret
// layer did not run; a request without the secret gets no mark.
func TestRequireSecretMarksTheServiceCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &EventRenderHandler{secrets: [][sha256.Size]byte{sha256.Sum256([]byte("s3cret"))}, admission: make(chan struct{}, 1)}
	marked := false
	r := gin.New()
	r.GET("/render/key", h.requireSecret, func(c *gin.Context) {
		marked = middleware.CredentialChecked(c, string(authz.SourceService(authz.ServiceRender)))
		c.Status(http.StatusOK)
	})
	for _, c := range []struct {
		secret string
		code   int
		marked bool
	}{{"s3cret", http.StatusOK, true}, {"wrong", http.StatusNotFound, false}} {
		marked = false
		req := httptest.NewRequest(http.MethodGet, "/render/key", nil)
		req.Header.Set(renderSecretHeader, c.secret)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != c.code || marked != c.marked {
			t.Errorf("secret %q: status %d marked %v, want %d %v", c.secret, w.Code, marked, c.code, c.marked)
		}
	}
}
