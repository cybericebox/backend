package clientaddr_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cybericebox/daemon/internal/delivery/controller/http/clientaddr"
)

func TestFromRequestIsThePeerAndNeverAHeader(t *testing.T) {
	cases := []struct{ name, remote, want string }{
		{"v4", "203.0.113.9:5000", "203.0.113.9"},
		{"v6", "[2001:db8::1]:5000", "2001:db8::1"},
		{"mapped v4", "[::ffff:203.0.113.9]:5000", "203.0.113.9"},
		{"bare host", "203.0.113.9", "203.0.113.9"},
		{"zone", "[fe80::1%eth0]:5000", ""},
		{"not an address", "example.test:80", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = c.remote
		for _, h := range []string{"X-Forwarded-For", "X-Real-IP", "Forwarded", "CF-Connecting-IP", "True-Client-IP"} {
			req.Header.Set(h, "198.51.100.7")
		}
		if got := clientaddr.FromRequest(req); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
	if clientaddr.FromRequest(nil) != "" {
		t.Error("nil request")
	}
}
