package assistant_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/daemon/internal/delivery/infrastructure/brain/brainfake"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
	"github.com/cybericebox/daemon/internal/useCase/assistant"
	"github.com/cybericebox/daemon/pkg/agentcrypto"
	"github.com/cybericebox/daemon/pkg/secret"
)

const (
	keyA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	keyB = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

var (
	userOne = uuid.FromStringOrNil("11111111-1111-4111-8111-111111111111")
	userTwo = uuid.FromStringOrNil("22222222-2222-4222-8222-222222222222")
)

func cipher(t *testing.T, key string) *secret.Cipher {
	t.Helper()
	c, err := secret.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// memStore is the connection store in memory.
type memStore struct {
	mu    sync.Mutex
	conns map[string]assistantModel.Connection
	// lost makes Create report that another replica won.
	lost bool
}

func newMemStore() *memStore { return &memStore{conns: map[string]assistantModel.Connection{}} }

func (s *memStore) Get(_ context.Context, endpoint string) (assistantModel.Connection, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conns[endpoint]
	return c, ok, nil
}

func (s *memStore) Create(_ context.Context, c assistantModel.Connection) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lost {
		return false, nil
	}
	if _, ok := s.conns[c.Endpoint]; ok {
		return false, nil
	}
	s.conns[c.Endpoint] = c
	return true, nil
}

func (s *memStore) SetCertificate(_ context.Context, endpoint, tenant, certPEM, keyCT, caPEM string, notAfter, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conns[endpoint]
	if !ok || c.Tenant != tenant {
		return false, nil
	}
	c.CertPEM, c.KeyCiphertext, c.CAPEM, c.CertNotAfter, c.UpdatedAt = certPEM, keyCT, caPEM, notAfter, at
	s.conns[endpoint] = c
	return true, nil
}

type rig struct {
	fake  *brainfake.Fake
	store *memStore
	uc    *assistant.UseCase
	clock *clock
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newRig(t *testing.T, mutate func(*assistant.Dependencies)) *rig {
	t.Helper()
	f := brainfake.New()
	t.Cleanup(f.Server.Close)
	r := &rig{fake: f, store: newMemStore(), clock: &clock{t: time.Now()}}
	caFile := filepath.Join(t.TempDir(), "brain-ca.crt")
	if err := os.WriteFile(caFile, f.CAPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	d := assistant.Dependencies{
		Store: r.store, Cipher: cipher(t, keyA), Subjects: newMemSubjects(),
		// The fake's CA is not in the system roots: BRAIN_CA_FILE names it.
		Config: assistant.Config{Endpoint: f.Endpoint(), EnrollmentToken: brainfake.Token, CAFile: caFile},
		Now:    r.clock.now,
	}
	if mutate != nil {
		mutate(&d)
	}
	r.uc = assistant.New(d)
	return r
}

// memSubjects is the pseudonym store in memory.
type memSubjects struct {
	mu    sync.Mutex
	rows  map[uuid.UUID]uuid.UUID
	delay time.Duration
}

func newMemSubjects() *memSubjects { return &memSubjects{rows: map[uuid.UUID]uuid.UUID{}} }

func (m *memSubjects) Get(_ context.Context, user uuid.UUID) (uuid.UUID, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.rows[user]
	return s, ok, nil
}

func (m *memSubjects) Create(_ context.Context, user, subject uuid.UUID, _ time.Time) (bool, error) {
	time.Sleep(m.delay)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[user]; ok {
		return false, nil
	}
	m.rows[user] = subject
	return true, nil
}

func subjectOf(t *testing.T, uc *assistant.UseCase, user uuid.UUID) string {
	t.Helper()
	s, err := uc.Subject(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSubjectIsARandomStoredPseudonymPerUser(t *testing.T) {
	store := newMemSubjects()
	uc := assistant.New(assistant.Dependencies{Subjects: store})
	a1, a2, b := subjectOf(t, uc, userOne), subjectOf(t, uc, userOne), subjectOf(t, uc, userTwo)
	if a1 == "" || a1 != a2 || a1 == b {
		t.Fatalf("subjects: %q %q %q", a1, a2, b)
	}
	if _, err := uuid.FromString(a1); err != nil || strings.Contains(a1, userOne.String()) || strings.Contains(a1, uuidHex(userOne)) {
		t.Fatalf("the subject must be an opaque random id, got %q", a1)
	}
	// Not derived from the user id: the same user in another store gets another pseudonym.
	if other := subjectOf(t, assistant.New(assistant.Dependencies{Subjects: newMemSubjects()}), userOne); other == a1 {
		t.Fatal("a pseudonym must be random, not a function of the user id")
	}
}

func TestSubjectSurvivesKeyRotationAndRestartButNotDeletionOfTheRow(t *testing.T) {
	store := newMemSubjects()
	before := subjectOf(t, assistant.New(assistant.Dependencies{Subjects: store, Cipher: cipher(t, keyA)}), userOne)
	// Another platform key and a fresh process (empty cache): the stored pseudonym is the same.
	after := subjectOf(t, assistant.New(assistant.Dependencies{Subjects: store, Cipher: cipher(t, keyB)}), userOne)
	if before != after {
		t.Fatalf("rotation changed the pseudonym: %q %q", before, after)
	}
	// The account's personal data was purged: the row is gone, a later use starts a new pseudonym, unlinked.
	delete(store.rows, userOne)
	if again := subjectOf(t, assistant.New(assistant.Dependencies{Subjects: store}), userOne); again == before {
		t.Fatal("after the row was deleted the old pseudonym must not come back")
	}
}

func TestSubjectNeedsNoSecretsKey(t *testing.T) {
	if subjectOf(t, assistant.New(assistant.Dependencies{Subjects: newMemSubjects()}), userOne) == "" {
		t.Fatal("no secrets key, still a subject")
	}
	if _, err := assistant.New(assistant.Dependencies{}).Subject(context.Background(), userOne); err == nil {
		t.Fatal("without a store there is no subject")
	}
}

func TestFirstUseAtOnceGivesEveryRequestTheSameSubject(t *testing.T) {
	store := newMemSubjects()
	store.delay = 20 * time.Millisecond
	uc := assistant.New(assistant.Dependencies{Subjects: store})
	results := make(chan string, 8)
	for range 8 {
		go func() {
			s, _ := uc.Subject(context.Background(), userOne)
			results <- s
		}()
	}
	first := <-results
	for range 7 {
		if got := <-results; got != first || got == "" {
			t.Fatalf("two subjects for one user: %q %q", first, got)
		}
	}
}

func uuidHex(u uuid.UUID) string { return strings.ReplaceAll(u.String(), "-", "") }

func TestBootstrapEnrollsOnceAndSealsTheKey(t *testing.T) {
	r := newRig(t, nil)
	enrolled, err := r.uc.BootstrapBrain(context.Background())
	if err != nil || !enrolled {
		t.Fatalf("bootstrap: %v %v", enrolled, err)
	}
	conn, found, _ := r.store.Get(context.Background(), r.fake.Endpoint())
	if !found || conn.Tenant != brainfake.Tenant || conn.CertPEM == "" {
		t.Fatalf("stored connection: %+v", conn)
	}
	if strings.Contains(conn.KeyCiphertext, "PRIVATE KEY") || conn.KeyCiphertext == "" {
		t.Fatal("the private key must be stored as ciphertext")
	}
	// The ciphertext opens only with its own context (the endpoint).
	if _, err = cipher(t, keyA).DecryptWithContext(conn.KeyCiphertext, assistantModel.SecretContext("other:1", "key")); err == nil {
		t.Fatal("the sealed key opened with another context")
	}
	plain, err := cipher(t, keyA).DecryptWithContext(conn.KeyCiphertext, assistantModel.SecretContext(conn.Endpoint, "key"))
	if err != nil || agentcrypto.MatchesKey(conn.CertPEM, string(plain)) != nil {
		t.Fatalf("the sealed key must match the stored certificate: %v", err)
	}

	again, err := r.uc.BootstrapBrain(context.Background())
	if err != nil || again || r.fake.Enrolls() != 1 {
		t.Fatalf("a stored connection is not enrolled again: again=%v err=%v enrolls=%d", again, err, r.fake.Enrolls())
	}
}

func TestBootstrapIsOffWithoutEndpointAndUnavailableWithoutTheSecretsKey(t *testing.T) {
	off := newRig(t, func(d *assistant.Dependencies) { d.Config.Endpoint = "" })
	if ok, err := off.uc.BootstrapBrain(context.Background()); ok || err != nil || off.uc.Enabled() {
		t.Fatalf("no endpoint: %v %v", ok, err)
	}
	nokey := newRig(t, func(d *assistant.Dependencies) { d.Cipher = nil })
	ok, err := nokey.uc.BootstrapBrain(context.Background())
	if ok || !errors.Is(err, assistantModel.ErrAssistantUnavailable.Err()) || nokey.uc.Enabled() {
		t.Fatalf("no cipher: ok=%v err=%v", ok, err)
	}
	if nokey.fake.Enrolls() != 0 {
		t.Fatal("nothing may be sent to the brain without a way to seal the key")
	}
	_, err = nokey.uc.Proxy(context.Background(), userOne, assistant.Call{Method: http.MethodGet, Path: "/v1/assistant/ideas"})
	if !errors.Is(err, assistantModel.ErrAssistantUnavailable.Err()) {
		t.Fatalf("proxy without the secrets key: %v", err)
	}
}

func TestBootstrapFailsClearlyOnABadTokenAndKeepsNothing(t *testing.T) {
	r := newRig(t, func(d *assistant.Dependencies) { d.Config.EnrollmentToken = "wrong" })
	if ok, err := r.uc.BootstrapBrain(context.Background()); ok || err == nil {
		t.Fatalf("a bad token must fail: %v %v", ok, err)
	}
	if _, found, _ := r.store.Get(context.Background(), r.fake.Endpoint()); found {
		t.Fatal("a failed enrollment must store nothing")
	}
	empty := newRig(t, func(d *assistant.Dependencies) { d.Config.EnrollmentToken = "" })
	if _, err := empty.uc.BootstrapBrain(context.Background()); err == nil {
		t.Fatal("no token and no stored connection must be reported")
	}
}

func TestBootstrapLosingTheRaceToAnotherReplicaIsNotAnError(t *testing.T) {
	r := newRig(t, nil)
	r.store.lost = true
	ok, err := r.uc.BootstrapBrain(context.Background())
	if ok || err != nil {
		t.Fatalf("lost race: %v %v", ok, err)
	}
}

func TestRenewIsDueAfterTwoThirdsOfTheLifetimeAndReplacesCertificateAndKey(t *testing.T) {
	r := newRig(t, nil)
	r.fake.CertTTL = 30 * 24 * time.Hour
	if _, err := r.uc.BootstrapBrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, _, _ := r.store.Get(context.Background(), r.fake.Endpoint())

	if ok, err := r.uc.RenewBrainCertificate(context.Background()); ok || err != nil {
		t.Fatalf("a fresh certificate is not renewed: %v %v", ok, err)
	}
	// Past two thirds of (30 days + the 1h the fake backdates it).
	r.clock.advance(21 * 24 * time.Hour)
	ok, err := r.uc.RenewBrainCertificate(context.Background())
	if err != nil || !ok || r.fake.Renews() != 1 {
		t.Fatalf("renew: ok=%v err=%v renews=%d", ok, err, r.fake.Renews())
	}
	after, _, _ := r.store.Get(context.Background(), r.fake.Endpoint())
	if after.CertPEM == before.CertPEM || after.KeyCiphertext == before.KeyCiphertext || after.Tenant != before.Tenant {
		t.Fatal("the renewal must replace the certificate and the key and keep the tenant")
	}
	plain, err := cipher(t, keyA).DecryptWithContext(after.KeyCiphertext, assistantModel.SecretContext(after.Endpoint, "key"))
	if err != nil || agentcrypto.MatchesKey(after.CertPEM, string(plain)) != nil {
		t.Fatalf("the new key must match the new certificate: %v", err)
	}
}

func TestRenewFailureKeepsTheStoredConnection(t *testing.T) {
	r := newRig(t, nil)
	r.fake.CertTTL = 30 * 24 * time.Hour
	if _, err := r.uc.BootstrapBrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, _, _ := r.store.Get(context.Background(), r.fake.Endpoint())
	r.clock.advance(25 * 24 * time.Hour)
	r.fake.FailRenewWith(http.StatusServiceUnavailable)
	if ok, err := r.uc.RenewBrainCertificate(context.Background()); ok || err == nil {
		t.Fatalf("a refused renewal must fail: %v %v", ok, err)
	}
	after, _, _ := r.store.Get(context.Background(), r.fake.Endpoint())
	if after != before {
		t.Fatal("a failed renewal must not touch the stored connection")
	}
}

func TestClientIsReloadedAfterTheLeaderRenewed(t *testing.T) {
	r := newRig(t, nil)
	r.fake.CertTTL = 30 * 24 * time.Hour
	var seen []string
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen = append(seen, req.TLS.PeerCertificates[0].SerialNumber.String())
		_, _ = w.Write([]byte("{}"))
	})
	if _, err := r.uc.BootstrapBrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	call := func() {
		t.Helper()
		resp, err := r.uc.Proxy(context.Background(), userOne, assistant.Call{Method: http.MethodGet, Path: "/v1/assistant/ideas"})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	call()
	r.clock.advance(21 * 24 * time.Hour)
	if _, err := r.uc.RenewBrainCertificate(context.Background()); err != nil {
		t.Fatal(err)
	}
	call()
	if len(seen) != 2 || seen[0] == seen[1] {
		t.Fatalf("the call after the renewal must present the new certificate: %v", seen)
	}
}

// ── the brain's CA is pinned after the first enrollment ─────────────────────────

func TestEnrollmentPinsTheCAThatTheBrainReturned(t *testing.T) {
	r := newRig(t, nil)
	if _, err := r.uc.BootstrapBrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	conn, _, _ := r.store.Get(context.Background(), r.fake.Endpoint())
	if conn.CAPEM != string(r.fake.CAPEM) {
		t.Fatal("the stored CA must be the one the brain returned")
	}
}

func TestAServerOfAnotherCAIsRejectedAfterEnrollmentEvenWhenBRAINCAFileTrustsIt(t *testing.T) {
	other := brainfake.New()
	defer other.Server.Close()
	r := newRig(t, func(d *assistant.Dependencies) {
		// BRAIN_CA_FILE trusts both brains: it is only for the first enrollment.
		both, err := os.ReadFile(d.Config.CAFile)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(d.Config.CAFile, append(both, other.CAPEM...), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := r.uc.BootstrapBrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	other.Assistant = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{}")) })
	call := func() error {
		resp, err := r.uc.Proxy(context.Background(), userOne, assistant.Call{Method: http.MethodGet, Path: "/v1/assistant/ideas"})
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{}")) })
	if err := call(); err != nil {
		t.Fatalf("the enrolled brain works: %v", err)
	}

	// Another brain, with a certificate of its own CA, takes the address.
	addr := r.fake.Server.Listener.Addr().String()
	r.fake.Server.Close()
	var l net.Listener
	var err error
	for range 40 {
		if l, err = net.Listen("tcp", addr); err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Skipf("cannot take over the address: %v", err)
	}
	imposter := brainfake.NewOn(l)
	defer imposter.Server.Close()
	imposter.Assistant = other.Assistant
	if err = call(); !errors.Is(err, assistantModel.ErrAssistantBrainUnreachable.Err()) {
		t.Fatalf("a server of another CA must be refused, got %v", err)
	}
}

func TestRenewalMayRotateTheCAAndThePinMovesWithIt(t *testing.T) {
	r := newRig(t, nil)
	r.fake.CertTTL = 30 * 24 * time.Hour
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{}")) })
	if _, err := r.uc.BootstrapBrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, _, _ := r.store.Get(context.Background(), r.fake.Endpoint())
	r.fake.RotateCAOnNextRenew()
	r.clock.advance(21 * 24 * time.Hour)
	if ok, err := r.uc.RenewBrainCertificate(context.Background()); err != nil || !ok {
		t.Fatalf("renew: %v %v", ok, err)
	}
	after, _, _ := r.store.Get(context.Background(), r.fake.Endpoint())
	if after.CAPEM == before.CAPEM || after.CAPEM != string(r.fake.CurrentCAPEM()) {
		t.Fatal("the new CA must replace the pinned one")
	}
	resp, err := r.uc.Proxy(context.Background(), userOne, assistant.Call{Method: http.MethodGet, Path: "/v1/assistant/ideas"})
	if err != nil {
		t.Fatalf("calls after the rotation: %v", err)
	}
	_ = resp.Body.Close()
}

func TestRenewalWhoseCertificateDoesNotChainToTheReturnedCAIsRefused(t *testing.T) {
	other := brainfake.New()
	defer other.Server.Close()
	r := newRig(t, nil)
	r.fake.CertTTL = 30 * 24 * time.Hour
	if _, err := r.uc.BootstrapBrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, _, _ := r.store.Get(context.Background(), r.fake.Endpoint())
	r.fake.ReturnCAOnRenew(other.CAPEM)
	r.clock.advance(21 * 24 * time.Hour)
	if ok, err := r.uc.RenewBrainCertificate(context.Background()); ok || err == nil {
		t.Fatalf("a certificate that does not chain to the returned CA must not be stored: %v %v", ok, err)
	}
	if after, _, _ := r.store.Get(context.Background(), r.fake.Endpoint()); after != before {
		t.Fatal("the stored connection and its pin must stay as they were")
	}
}
