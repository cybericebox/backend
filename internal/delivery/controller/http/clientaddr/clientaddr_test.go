package clientaddr_test

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cybericebox/daemon/internal/delivery/controller/http/clientaddr"
)

var verified = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{}}}}

func TestFromRequestIsThePeerAndNeverAForwardingHeader(t *testing.T) {
	cases := []struct{ name, remote, want string }{
		{"v4", "203.0.113.9:5000", "203.0.113.9"},
		{"v6", "[2001:db8::1]:5000", "2001:db8::1"},
		{"mapped v4", "[::ffff:203.0.113.9]:5000", "203.0.113.9"},
		{"bare host", "203.0.113.9", "203.0.113.9"},
		{"zone", "[fe80::1%eth0]:5000", ""},
		{"not an address", "example.test:80", ""},
		{"empty", "", ""},
	}
	for _, state := range []*tls.ConnectionState{nil, {}, {PeerCertificates: []*x509.Certificate{{}}}} {
		for _, c := range cases {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = c.remote
			req.TLS = state
			for _, h := range []string{"X-Forwarded-For", "X-Real-IP", "Forwarded", "CF-Connecting-IP", "True-Client-IP"} {
				req.Header.Set(h, "198.51.100.7")
			}
			if got := clientaddr.FromRequest(req); got != c.want {
				t.Errorf("%s: got %q want %q", c.name, got, c.want)
			}
		}
	}
	if clientaddr.FromRequest(nil) != "" {
		t.Error("nil request")
	}
}

func TestCloudflareHeaderIsReadOnlyWithAVerifiedChain(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		want   string
	}{
		{"v4", []string{"198.51.100.7"}, "198.51.100.7"},
		{"v6", []string{"2001:db8::1"}, "2001:db8::1"},
		{"mapped", []string{"::ffff:198.51.100.7"}, "198.51.100.7"},
		{"spaces", []string{"  198.51.100.7 "}, "198.51.100.7"},
		{"missing", nil, "10.0.0.5"},
		{"garbage", []string{"not-an-ip"}, "10.0.0.5"},
		{"list", []string{"1.2.3.4, 5.6.7.8"}, "10.0.0.5"},
		{"port", []string{"1.2.3.4:80"}, "10.0.0.5"},
		{"bracketed v6 with port", []string{"[2001:db8::1]:80"}, "10.0.0.5"},
		{"zone", []string{"fe80::1%eth0"}, "10.0.0.5"},
		{"two header lines", []string{"1.2.3.4", "5.6.7.8"}, "10.0.0.5"},
		{"empty", []string{""}, "10.0.0.5"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.5:4000"
		req.TLS = verified
		req.Header["Cf-Connecting-Ip"] = c.values
		if got := clientaddr.FromRequest(req); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
		req.TLS = &tls.ConnectionState{}
		if got := clientaddr.FromRequest(req); got != "10.0.0.5" {
			t.Errorf("%s: header read without a verified chain: %q", c.name, got)
		}
	}
}
