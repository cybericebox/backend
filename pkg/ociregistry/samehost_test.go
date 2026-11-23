package ociregistry

import (
	"context"
	"github.com/google/go-containerregistry/pkg/name"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type recordTransport struct{ got []string }

func (r *recordTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.got = append(r.got, req.URL.Host+" "+req.Header.Get("Authorization"))
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
}

// With SameHostCredentials the basic credential goes to the registry host only: a token realm on another host gets the
// request without it (the token exchange of go-containerregistry sends it to whatever realm the registry names).
func TestSameHostCredentialsKeepTheCredentialFromAnotherRealm(t *testing.T) {
	base := &recordTransport{}
	rt := sameHostAuth{base: base, host: "registry.example"}
	for _, u := range []string{"https://registry.example/token", "https://auth.elsewhere.example/token"} {
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		req.SetBasicAuth("brain", "secret")
		if _, err := rt.RoundTrip(req); err != nil {
			t.Fatal(err)
		}
	}
	bearer, _ := http.NewRequest(http.MethodGet, "https://cdn.example/blob", nil)
	bearer.Header.Set("Authorization", "Bearer t")
	_, _ = rt.RoundTrip(bearer)
	if !strings.HasPrefix(base.got[0], "registry.example Basic ") {
		t.Fatalf("the registry host keeps it: %q", base.got[0])
	}
	if base.got[1] != "auth.elsewhere.example " {
		t.Fatalf("another host gets no basic credential: %q", base.got[1])
	}
	if base.got[2] != "cdn.example Bearer t" {
		t.Fatalf("only a basic credential is dropped: %q", base.got[2])
	}
	c := New(Options{Host: "registry.example", Repository: "a/b"}).WithSameHostCredentials()
	if _, ok := c.baseTransport(mustRegistry(t, "registry.example")).(sameHostAuth); !ok {
		t.Fatal("the client puts the guard under its token transport")
	}
}

// Each request has its own timeout.
func TestRequestTimeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	c := New(Options{Host: strings.TrimPrefix(slow.URL, "http://"), Repository: "a/b", Insecure: true, AllowInternal: true}).WithRequestTimeout(100 * time.Millisecond)
	start := time.Now()
	if _, err := c.ImageConfigs(context.Background(), "1"); err == nil {
		t.Fatal("a request over its timeout fails")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("the request was not bounded: %v", time.Since(start))
	}
}

func mustRegistry(t *testing.T, host string) name.Registry {
	t.Helper()
	reg, err := name.NewRegistry(host)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}
