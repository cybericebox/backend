package assistant_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cybericebox/backend/internal/useCase/assistant"
)

// startBrainMTLS starts the real brain in its mutual-TLS mode: it runs its own CA, enrolls the daemon on the one-time token
// and writes its CA certificate for BRAIN_CA_FILE. It returns the address and that file.
func startBrainMTLS(t *testing.T, dir, modelURL, token string) (addr, caFile string) {
	t.Helper()
	caFile = filepath.Join(t.TempDir(), "brain-ca.crt")
	addr = startBrainArgs(t, dir, modelURL, "-enroll-token", token, "-enroll-tenant", "daemon", "-ca-out", caFile)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if info, err := os.Stat(caFile); err == nil && info.Size() > 0 {
			return addr, caFile
		}
		if time.Now().After(deadline) {
			t.Fatal("the brain did not write its CA")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestTheRealBrainInMutualTLSMode(t *testing.T) {
	dir := brainCheckout(t)
	model := modelServer(t, `{"thought":"the file says it","submit":"ICE{real}"}`)
	const token = "one-time-token-for-the-contract-test"
	addr, caFile := startBrainMTLS(t, dir, model.URL, token)
	config := assistant.Config{Endpoint: addr, EnrollmentToken: token, CAFile: caFile}
	ctx := context.Background()

	// 1. our own enrollment code: the CA of BRAIN_CA_FILE for the first call, then the brain's CA is the pin
	r := newRunRig(t, func(d *assistant.Dependencies) { d.Config = config })
	conn, found, err := r.store.Get(ctx, addr)
	if err != nil || !found || conn.Tenant != "daemon" || conn.CAPEM == "" || conn.CertPEM == "" {
		t.Fatalf("enrolled: %v %v %+v", found, err, conn)
	}

	// 2. the static grader flow over mutual TLS, with no reverse proxy in between
	staticMaterials(r)
	staticGraderRun(t, r)

	// 3. the one-time token works once
	again := newRig(t, func(d *assistant.Dependencies) { d.Config = config })
	if enrolled, err := again.uc.BootstrapBrain(ctx); err == nil || enrolled {
		t.Fatalf("a second enrollment with the same token must fail: %v %v", enrolled, err)
	}

	// 4. renewal keeps the tenant (the CN); the certificate is new and the connection still works
	r.clock.advance(70 * 24 * time.Hour) // past two thirds of the 90 days the brain issues
	renewed, err := r.uc.RenewBrainCertificate(ctx)
	if err != nil || !renewed {
		t.Fatalf("renewal: %v %v", renewed, err)
	}
	after, _, _ := r.store.Get(ctx, addr)
	if after.Tenant != "daemon" || after.CertPEM == conn.CertPEM {
		t.Fatalf("the certificate is new and names the same tenant: %q", after.Tenant)
	}
	staticGraderRun(t, r)
}
