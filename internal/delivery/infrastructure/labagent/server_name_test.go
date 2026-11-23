package labagent

import (
	"context"
	"testing"
	"time"

	labpb "github.com/cybericebox/laboratory/pkg/agent/protobuf"
)

// The agent's certificate names ctl.example.test, but it is reached at another address (here 127.0.0.1, in a
// cluster its Service name): the call verifies against the configured server name, and without one against the
// host of the address, which the certificate does not name.
const agentCertName = "ctl.example.test"

func TestEnrollVerifiesTheAgentAgainstTheServerName(t *testing.T) {
	agent := &enrollServer{}
	endpoint, ca := startAgentFor(t, agent, []string{agentCertName}, nil)
	if cert, err := Enroll(context.Background(), endpoint, agentCertName, ca, "good-token", "CSR", "PUB", "k"); err != nil || cert == "" {
		t.Fatalf("enroll with the server name = %q, %v", cert, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := Enroll(ctx, endpoint, "", ca, "good-token", "CSR", "PUB", "k"); err == nil {
		t.Fatal("without the server name the certificate does not name the address: the call must fail")
	}
	if _, err := Enroll(ctx, endpoint, "other.example.test", ca, "good-token", "CSR", "PUB", "k"); err == nil {
		t.Fatal("a server name the certificate does not carry must fail")
	}
}

func TestDialVerifiesTheAgentAgainstTheServerName(t *testing.T) {
	endpoint, ca := startAgentFor(t, &enrollServer{}, []string{agentCertName}, nil)
	cert, key := testKeyPair(t)
	call := func(serverName string) error {
		c, err := NewFromConnection(Connection{Endpoint: endpoint, ServerName: serverName, CertPEM: cert, KeyPEM: key, CAPEM: ca}, "prod")
		if err != nil {
			return err
		}
		defer c.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err = c.Client.Enroll(ctx, &labpb.EnrollRequest{Token: "good-token", CsrPem: "CSR"})
		return err
	}
	if err := call(agentCertName); err != nil {
		t.Fatalf("dial with the server name: %v", err)
	}
	if err := call(""); err == nil {
		t.Fatal("without the server name the certificate does not name the address: the call must fail")
	}
}
