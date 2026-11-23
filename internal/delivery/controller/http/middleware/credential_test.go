package middleware_test

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cybericebox/backend/internal/delivery/controller/http/middleware"
)

func TestCredentialIsCheckedOnlyAfterItsLayerMarkedIt(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if middleware.CredentialChecked(c, "service(render)") {
		t.Fatal("a fresh request reports a checked credential")
	}
	middleware.MarkCredentialChecked(c, "service(render)")
	if !middleware.CredentialChecked(c, "service(render)") {
		t.Fatal("the mark is not seen")
	}
	if middleware.CredentialChecked(c, "bearer(live-screen)") {
		t.Fatal("the mark of one credential stands for another")
	}
}
