package brain_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/cybericebox/daemon/internal/delivery/infrastructure/brain"
	"github.com/cybericebox/daemon/internal/delivery/infrastructure/brain/brainfake"
	"github.com/cybericebox/daemon/pkg/agentcrypto"
)

func enrolled(t *testing.T, f *brainfake.Fake) (*brain.Client, agentcrypto.ClientKey) {
	t.Helper()
	key, err := agentcrypto.NewClientKey()
	if err != nil {
		t.Fatal(err)
	}
	out, err := brain.Enroll(context.Background(), f.Endpoint(), f.CAPEM, brainfake.Token, key.CSRPEM)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	c, err := brain.NewClient(f.Endpoint(), []byte(out.CertPEM), []byte(key.KeyPEM), f.CAPEM)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	t.Cleanup(c.Close)
	return c, key
}

func TestEnrollIssuesACertificateForOurKeyAndTheTokenIsOneTime(t *testing.T) {
	f := brainfake.New()
	defer f.Server.Close()
	key, _ := agentcrypto.NewClientKey()

	out, err := brain.Enroll(context.Background(), f.Endpoint(), f.CAPEM, brainfake.Token, key.CSRPEM)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := agentcrypto.ReadCertificate(out.CertPEM)
	if err != nil || cert.Tenant != brainfake.Tenant {
		t.Fatalf("certificate: %+v %v", cert, err)
	}
	if err = agentcrypto.MatchesKey(out.CertPEM, key.KeyPEM); err != nil {
		t.Fatal(err)
	}

	_, err = brain.Enroll(context.Background(), f.Endpoint(), f.CAPEM, brainfake.Token, key.CSRPEM)
	if !errors.Is(err, brain.ErrEnrollmentDenied) {
		t.Fatalf("a used token must be denied, got %v", err)
	}
}

func TestEnrollWithAWrongTokenIsDeniedAndWithAnUntrustedServerIsUnreachable(t *testing.T) {
	f := brainfake.New()
	defer f.Server.Close()
	key, _ := agentcrypto.NewClientKey()

	if _, err := brain.Enroll(context.Background(), f.Endpoint(), f.CAPEM, "nope", key.CSRPEM); !errors.Is(err, brain.ErrEnrollmentDenied) {
		t.Fatalf("wrong token: %v", err)
	}
	// No CA given: the system roots do not know the fake CA.
	if _, err := brain.Enroll(context.Background(), f.Endpoint(), nil, brainfake.Token, key.CSRPEM); !errors.Is(err, brain.ErrUnreachable) {
		t.Fatalf("untrusted server: %v", err)
	}
}

func TestRenewGoesOverMutualTLSWithTheCurrentCertificate(t *testing.T) {
	f := brainfake.New()
	defer f.Server.Close()
	c, _ := enrolled(t, f)

	fresh, _ := agentcrypto.NewClientKey()
	out, err := c.Renew(context.Background(), fresh.CSRPEM)
	if err != nil {
		t.Fatal(err)
	}
	if err = agentcrypto.MatchesKey(out.CertPEM, fresh.KeyPEM); err != nil {
		t.Fatalf("the renewed certificate is for the new key: %v", err)
	}
	if f.Renews() != 1 {
		t.Fatalf("renews = %d", f.Renews())
	}
}

func TestDoMapsTheErrorEnvelopeAndKeepsTheBodyOfASuccess(t *testing.T) {
	f := brainfake.New()
	defer f.Server.Close()
	f.Assistant = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/assistant/ok":
			_, _ = w.Write([]byte("hello"))
		case "/v1/assistant/missing":
			brainfake.Fail(w, http.StatusNotFound, "session_not_found", "no such session")
		default:
			http.Error(w, "plain text, not an envelope", http.StatusBadGateway)
		}
	})
	c, _ := enrolled(t, f)

	resp, err := c.Do(context.Background(), brain.Request{Method: http.MethodGet, Path: "/v1/assistant/ok"})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "hello" {
		t.Fatalf("body = %q", body)
	}

	var api *brain.APIError
	_, err = c.Do(context.Background(), brain.Request{Method: http.MethodGet, Path: "/v1/assistant/missing"})
	if !errors.As(err, &api) || api.Status != 404 || api.Code != "session_not_found" || api.Message != "no such session" {
		t.Fatalf("envelope: %#v", err)
	}
	_, err = c.Do(context.Background(), brain.Request{Method: http.MethodGet, Path: "/v1/assistant/other"})
	if !errors.As(err, &api) || api.Status != 502 || api.Code != "" {
		t.Fatalf("a body that is not an envelope keeps the status and has no code: %#v", err)
	}
}

func TestDoWithoutAClientCertificateIsRefusedByTheBrain(t *testing.T) {
	f := brainfake.New()
	defer f.Server.Close()
	f.Assistant = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("secret")) })
	// A server-only client: the Enroll transport, used through renew's sibling path, cannot reach assistant routes.
	key, _ := agentcrypto.NewClientKey()
	other := brainfake.New()
	defer other.Server.Close()
	out, err := brain.Enroll(context.Background(), other.Endpoint(), other.CAPEM, brainfake.Token, key.CSRPEM)
	if err != nil {
		t.Fatal(err)
	}
	// A certificate issued by another CA does not pass this brain's client verification.
	c, err := brain.NewClient(f.Endpoint(), []byte(out.CertPEM), []byte(key.KeyPEM), f.CAPEM)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	resp, err := c.Do(context.Background(), brain.Request{Method: http.MethodGet, Path: "/v1/assistant/x"})
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("a foreign certificate must not reach the assistant routes")
	}
}

func TestNewClientRejectsMismatchedMaterial(t *testing.T) {
	f := brainfake.New()
	defer f.Server.Close()
	_, key := enrolled(t, f)
	other, _ := agentcrypto.NewClientKey()
	if _, err := brain.NewClient(f.Endpoint(), []byte("not a cert"), []byte(key.KeyPEM), nil); err == nil {
		t.Fatal("accepted garbage certificate")
	}
	if _, err := brain.NewClient(f.Endpoint(), []byte(key.CSRPEM), []byte(other.KeyPEM), nil); err == nil {
		t.Fatal("accepted a mismatched pair")
	}
	if _, err := brain.NewClient(f.Endpoint(), []byte(key.CSRPEM), []byte(key.KeyPEM), []byte("not a CA")); err == nil {
		t.Fatal("accepted garbage CA")
	}
}
