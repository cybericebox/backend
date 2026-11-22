// Package assistant is the platform's side of the authoring assistant: the client connection to cyber-brain (one-time
// enrollment and renewal of the client certificate over HTTP, the mutual-TLS clients), the opaque per-user subject the brain
// binds its sessions to, the pass-through of the chat routes (HTTP/SSE), the save of a finished task into the catalog, and the
// machine control plane over gRPC (pkg/brainapi/v1): the runs, the outbox, the statistics and the opponents. The brain itself
// is an external service; nothing here interprets what the chat says.
package assistant

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"

	"github.com/cybericebox/backend/internal/delivery/infrastructure/brain"
	"github.com/cybericebox/backend/internal/model"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	adminAuditUseCase "github.com/cybericebox/backend/internal/useCase/adminAudit"
	exerciseUseCase "github.com/cybericebox/backend/internal/useCase/exercise"
	"github.com/cybericebox/backend/pkg/agentcrypto"
	"github.com/cybericebox/backend/pkg/secret"
)

const (
	// renewAt is the fraction of its lifetime after which the client certificate is renewed: the same two thirds as
	// the laboratory agents' certificates.
	renewAtNumerator, renewAtDenominator = 2, 3
	// MaintainInterval is how often the leader checks whether the certificate is due.
	MaintainInterval = 10 * time.Minute
	// connectionTTL is how long a replica trusts its loaded client before it reads the stored connection again: a
	// certificate renewed by the leader reaches the other replicas within it.
	connectionTTL = time.Minute
)

type (
	// Store is the persistence of the connection.
	Store interface {
		Get(ctx context.Context, endpoint string) (assistantModel.Connection, bool, error)
		Create(ctx context.Context, c assistantModel.Connection) (bool, error)
		// SetGRPCEndpoint stores the gRPC endpoint of a connection (false: no such connection, or it is that one already).
		SetGRPCEndpoint(ctx context.Context, endpoint, grpcEndpoint string, at time.Time) (bool, error)
		SetCertificate(ctx context.Context, endpoint, tenant, certPEM, keyCiphertext, caPEM string, notAfter, at time.Time) (bool, error)
	}

	// SubjectStore keeps the random pseudonym of each user.
	SubjectStore interface {
		Get(ctx context.Context, user uuid.UUID) (uuid.UUID, bool, error)
		Create(ctx context.Context, user, subject uuid.UUID, at time.Time) (bool, error)
	}

	// Config is what the deployment says about the brain.
	Config struct {
		// Endpoint is host:port of the brain's HTTP endpoint (the chat, the enrollment, the renewal); empty switches the
		// assistant off.
		Endpoint string
		// GRPCEndpoint is host:port of the brain's gRPC control plane (BRAIN_GRPC_ENDPOINT); it is stored with the connection.
		// Empty: the runs, the outbox and the statistics are off while the chat works.
		GRPCEndpoint    string
		EnrollmentToken string
		// CAFile verifies the brain's server certificate; empty means the system roots.
		CAFile string
	}

	// Enroller is the one-time enrollment call (brain.Enroll in production).
	Enroller func(ctx context.Context, endpoint string, caPEM []byte, token, csrPEM string) (assistantModel.Enrollment, error)

	// Dependencies of the use case.
	Dependencies struct {
		Store Store
		// Cipher seals the private key and derives the subject key; nil (PLATFORM_SECRETS_KEY unset) keeps the
		// assistant off.
		Cipher *secret.Cipher
		Config Config
		// Importer saves a finished task into the catalog; nil without exercises (tests).
		Importer Importer
		Files    FileStore
		// Stats gives the statistics aggregates (nil: collection is off everywhere).
		Stats StatsSource
		// StatsAdmin keeps the statistics collection modes (the platform defaults and the events').
		StatsAdmin StatsAdminStore
		// WatchMin and WatchMax bound the reconnect backoff of the outbox stream; AnswerPause is the first pause between
		// answer attempts (zero: 1s, 1min and 200ms).
		WatchMin, WatchMax, AnswerPause time.Duration
		// Saves remembers how a session was saved; Subjects keeps the users' pseudonyms.
		Saves    SaveStore
		Subjects SubjectStore
		// Audit records the admin audit entry of a task the assistant saved; nil: none.
		Audit  func(ctx context.Context, entry adminAuditUseCase.Entry) error
		Enroll Enroller
		Now    func() time.Time
		// NamesImages tells whether a task archive names device image files (those import in the background);
		// nil: the exercise use case's reader. A test seam.
		NamesImages func(archive []byte, lim exerciseModel.ArchiveLimits) bool
		// Runs keeps the runs and Labs gives them their test lab (nil: runs are unavailable). RunMaxDuration is the longest
		// a run lasts, RunGrace how long an unreachable brain is waited for, MaxActiveRuns how many runs a user may have at
		// once, RunPoll how often the leader looks at the runs and RunPause the first pause between retries (zero: the
		// defaults, 2s and 1s).
		Runs RunStore
		// Users gives the role of the user a lab request names (nil: labs are unavailable).
		Users UserLookup
		Labs  LabRunner
		// LabActivity gives the laboratory's activity figures of a run's lab for its lab facts (nil: not reported).
		LabActivity LabActivity
		// Tasks gives the import archive of a task version to a user who may edit it (provide_task); nil: not available.
		Tasks          TaskSource
		RunMaxDuration time.Duration
		RunGrace       time.Duration
		MaxActiveRuns  int
		RunPoll        time.Duration
		RunPause       time.Duration
		// LabSweep is how often the leader looks for the labs of opponent runs that outlived all their runs (zero: a minute).
		LabSweep time.Duration
		// OpponentBudget is what every opponent run may spend (the author never sets one); zero fields take the defaults.
		OpponentBudget assistantModel.RunBudget
	}

	// UseCase is the assistant.
	UseCase struct {
		store      Store
		cipher     *secret.Cipher
		cfg        Config
		enroll     Enroller
		now        func() time.Time
		importer   Importer
		files      FileStore
		saves      SaveStore
		stats      StatsSource
		statsAdmin StatsAdminStore

		watchMin, watchMax, answerPause time.Duration
		audit                           func(ctx context.Context, entry adminAuditUseCase.Entry) error

		namesImages func(archive []byte, lim exerciseModel.ArchiveLimits) bool

		runs              RunStore
		users             UserLookup
		labs              LabRunner
		activity          LabActivity
		tasks             TaskSource
		runMax, runGrace  time.Duration
		oppBudget         assistantModel.RunBudget
		maxActive         int
		runPoll, runPause time.Duration
		labSweep          time.Duration

		subjects     SubjectStore
		subjectCache sync.Map // user id -> subject: a pseudonym never changes while the account lives

		// outbox is the open outbox stream of this replica (the leader's), the one every answer goes up on.
		outbox outboxStream
		// reanswer holds the labs (run ids) whose answer the brain asked for again: it never got the first one.
		reanswer sync.Map
		// claims orders the claims of labs the brain asks for.
		claims claimTurns

		mu       sync.Mutex
		cached   *clients
		loadedAt time.Time
		cachedAt time.Time // UpdatedAt of the connection the cached clients were built from
	}

	// clients are the two clients of one stored connection: HTTP for the chat and the renewal, gRPC for the control plane (nil
	// while no gRPC endpoint is known).
	clients struct {
		http    *brain.Client
		control *brain.Control
	}
)

// New builds the use case. It reads nothing and dials nothing.
func New(d Dependencies) *UseCase {
	u := &UseCase{
		store: d.Store, cipher: d.Cipher, cfg: d.Config, enroll: d.Enroll, now: d.Now, importer: d.Importer, files: d.Files, saves: d.Saves, subjects: d.Subjects, audit: d.Audit, stats: d.Stats, statsAdmin: d.StatsAdmin,
		watchMin: orDefault(d.WatchMin, watchBackoffMin), watchMax: orDefault(d.WatchMax, watchBackoffMax), answerPause: orDefault(d.AnswerPause, 200*time.Millisecond),
		namesImages: d.NamesImages,
		runs:        d.Runs, users: d.Users, labs: d.Labs, activity: d.LabActivity, tasks: d.Tasks, maxActive: d.MaxActiveRuns,
		runMax: orDefault(d.RunMaxDuration, DefaultRunMaxDuration), runGrace: orDefault(d.RunGrace, DefaultRunGrace),
		oppBudget: d.OpponentBudget, runPoll: orDefault(d.RunPoll, 2*time.Second), runPause: orDefault(d.RunPause, time.Second),
		labSweep: orDefault(d.LabSweep, defaultLabSweep),
	}
	if u.maxActive <= 0 {
		u.maxActive = DefaultMaxActiveRuns
	}
	if u.namesImages == nil {
		u.namesImages = func(archive []byte, lim exerciseModel.ArchiveLimits) bool {
			return exerciseUseCase.ArchiveNamesImages(archive, "", lim)
		}
	}
	if u.enroll == nil {
		u.enroll = brain.Enroll
	}
	if u.now == nil {
		u.now = time.Now
	}
	return u
}

// Subject is the opaque pseudonym of a user the brain binds its sessions to: a random 128-bit id made on the user's
// first use and stored, so it does not depend on any key and survives key rotation. It is not the platform id.
// Deleting the account deletes the row, which cuts the link. Never log it next to the user id.
func (u *UseCase) Subject(ctx context.Context, user uuid.UUID) (string, error) {
	if u.subjects == nil {
		return "", assistantModel.ErrAssistantUnavailable.Err()
	}
	if cached, ok := u.subjectCache.Load(user); ok {
		if subject, isString := cached.(string); isString {
			return subject, nil
		}
	}
	subject, found, err := u.subjects.Get(ctx, user)
	if err == nil && !found {
		if _, err = u.subjects.Create(ctx, user, uuid.Must(uuid.NewV4()), u.now().UTC()); err == nil {
			// Two requests at once: the first row stays, both read it.
			subject, found, err = u.subjects.Get(ctx, user)
		}
	}
	if err != nil {
		return "", model.ErrPlatform.WithError(err).WithMessage("Failed to read the assistant subject").Err()
	}
	if !found {
		return "", model.ErrPlatform.WithMessage("The assistant subject was not stored").Err()
	}
	u.subjectCache.Store(user, subject.String())
	return subject.String(), nil
}

// Enabled reports whether the assistant is configured: a brain endpoint and the platform secrets key. It does not say
// whether the enrollment has happened yet.
func (u *UseCase) Enabled() bool {
	return strings.TrimSpace(u.cfg.Endpoint) != "" && u.cipher != nil
}

func (u *UseCase) endpoint() string { return strings.TrimSpace(u.cfg.Endpoint) }

func (u *UseCase) grpcEndpoint() string { return strings.TrimSpace(u.cfg.GRPCEndpoint) }

// BootstrapBrain enrolls the platform at the brain named by BRAIN_ENDPOINT with the one-time token when no connection
// for that endpoint is stored yet. It reports whether it enrolled. Without the platform secrets key it returns
// ErrAssistantUnavailable and the assistant stays off; any other failure is returned for the caller to log, the daemon
// starts anyway.
func (u *UseCase) BootstrapBrain(ctx context.Context) (bool, error) {
	endpoint := u.endpoint()
	if endpoint == "" {
		return false, nil
	}
	if u.cipher == nil {
		return false, assistantModel.ErrAssistantUnavailable.Err()
	}
	if conn, found, err := u.store.Get(ctx, endpoint); err != nil {
		return false, model.ErrPlatform.WithError(err).WithMessage("Failed to read the cyber-brain connection").Err()
	} else if found {
		// BRAIN_GRPC_ENDPOINT changed (or was added after the enrollment): the stored connection follows it.
		if grpc := u.grpcEndpoint(); grpc != "" && grpc != conn.GRPCEndpoint {
			if _, err = u.store.SetGRPCEndpoint(ctx, endpoint, grpc, u.now().UTC()); err != nil {
				return false, model.ErrPlatform.WithError(err).WithMessage("Failed to save the cyber-brain gRPC endpoint").Err()
			}
		}
		return false, nil
	}
	if strings.TrimSpace(u.cfg.EnrollmentToken) == "" {
		return false, errors.New("BRAIN_ENROLLMENT_TOKEN is empty: no connection to cyber-brain is stored and none can be enrolled")
	}
	ca, err := u.operatorCA()
	if err != nil {
		return false, err
	}
	key, err := agentcrypto.NewClientKey()
	if err != nil {
		return false, fmt.Errorf("generate the client key: %w", err)
	}
	out, err := u.enroll(ctx, endpoint, ca, strings.TrimSpace(u.cfg.EnrollmentToken), key.CSRPEM)
	if err != nil {
		return false, fmt.Errorf("enroll at cyber-brain: %w", err)
	}
	cert, err := checkCertificate(out.CertPEM, key.KeyPEM)
	if err != nil {
		return false, err
	}
	// BRAIN_CA_FILE (or the system roots) was trusted for this one call. From now on the brain's own CA, which signs both
	// its server certificate and our client certificate, is the one pinned trust anchor.
	if strings.TrimSpace(out.CAPEM) == "" {
		return false, errors.New("cyber-brain returned no CA to pin")
	}
	if err = verifyIssuedBy(out.CertPEM, out.CAPEM, u.now()); err != nil {
		return false, err
	}
	sealed, err := u.sealKey(endpoint, key.KeyPEM)
	if err != nil {
		return false, err
	}
	now := u.now().UTC()
	stored, err := u.store.Create(ctx, assistantModel.Connection{
		Endpoint: endpoint, GRPCEndpoint: u.grpcEndpoint(), Tenant: cert.Tenant, CertPEM: out.CertPEM, KeyCiphertext: sealed, CAPEM: out.CAPEM,
		CertNotAfter: cert.NotAfter, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return false, model.ErrPlatform.WithError(err).WithMessage("Failed to save the cyber-brain connection").Err()
	}
	// false: another replica enrolled first with the same token and stored its certificate; ours is not used.
	return stored, nil
}

// operatorCA reads BRAIN_CA_FILE (nil: the system roots).
func (u *UseCase) operatorCA() ([]byte, error) {
	if u.cfg.CAFile == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(u.cfg.CAFile)
	if err != nil {
		return nil, fmt.Errorf("cannot read BRAIN_CA_FILE: %w", err)
	}
	return raw, nil
}

// verifyIssuedBy checks the client certificate chains to the CA (as a client certificate) at the given time.
func verifyIssuedBy(certPEM, caPEM string, at time.Time) error {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(caPEM)) {
		return errors.New("cyber-brain returned a CA that holds no certificate")
	}
	var leaf *x509.Certificate
	inter := x509.NewCertPool()
	rest := []byte(certPEM)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return fmt.Errorf("cyber-brain returned an unreadable certificate: %w", err)
		}
		if leaf == nil {
			leaf = c
		} else {
			inter.AddCert(c)
		}
	}
	if leaf == nil {
		return errors.New("cyber-brain returned no certificate")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: at, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return fmt.Errorf("the client certificate does not chain to the CA cyber-brain returned: %w", err)
	}
	return nil
}

func checkCertificate(certPEM, keyPEM string) (agentcrypto.Certificate, error) {
	cert, err := agentcrypto.ReadCertificate(certPEM)
	if err != nil {
		return agentcrypto.Certificate{}, fmt.Errorf("cyber-brain returned an unusable certificate: %w", err)
	}
	if err = agentcrypto.MatchesKey(certPEM, keyPEM); err != nil {
		return agentcrypto.Certificate{}, fmt.Errorf("cyber-brain returned a certificate for another key: %w", err)
	}
	return cert, nil
}

func (u *UseCase) sealKey(endpoint, keyPEM string) (string, error) {
	sealed, err := u.cipher.EncryptWithContext([]byte(keyPEM), assistantModel.SecretContext(endpoint, "key"))
	if err != nil {
		return "", model.ErrPlatform.WithError(err).WithMessage("Failed to seal the cyber-brain client key").Err()
	}
	return sealed, nil
}

// brainCA is the CA pinned at enrollment (PEM): the tools device is given it to verify the brain's own credentials.
func (u *UseCase) brainCA(ctx context.Context) (string, error) {
	if !u.Enabled() {
		return "", assistantModel.ErrAssistantUnavailable.Err()
	}
	conn, found, err := u.store.Get(ctx, u.endpoint())
	if err != nil {
		return "", model.ErrPlatform.WithError(err).WithMessage("Failed to read the cyber-brain connection").Err()
	}
	if !found || strings.TrimSpace(conn.CAPEM) == "" {
		return "", assistantModel.ErrAssistantUnavailable.Err()
	}
	return conn.CAPEM, nil
}

// client returns the mutual-TLS HTTP client of the stored connection: the chat and the renewal.
func (u *UseCase) client(ctx context.Context) (*brain.Client, error) {
	c, err := u.connection(ctx)
	if err != nil {
		return nil, err
	}
	return c.http, nil
}

// control returns the mutual-TLS gRPC client of the brain's control plane. Without a gRPC endpoint (none stored and none
// configured) the control plane is unavailable.
func (u *UseCase) control(ctx context.Context) (*brain.Control, error) {
	c, err := u.connection(ctx)
	if err != nil {
		return nil, err
	}
	if c.control == nil {
		return nil, assistantModel.ErrAssistantUnavailable.Err()
	}
	return c.control, nil
}

// connection returns the clients of the stored connection. They are rebuilt when the stored connection changed (a renewal by
// the leader, a new gRPC endpoint), and the stored connection is re-read at most once per connectionTTL.
func (u *UseCase) connection(ctx context.Context) (*clients, error) {
	if !u.Enabled() {
		return nil, assistantModel.ErrAssistantUnavailable.Err()
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.cached != nil && u.now().Sub(u.loadedAt) < connectionTTL {
		return u.cached, nil
	}
	conn, found, err := u.store.Get(ctx, u.endpoint())
	if err != nil {
		return nil, model.ErrPlatform.WithError(err).WithMessage("Failed to read the cyber-brain connection").Err()
	}
	if !found {
		return nil, assistantModel.ErrAssistantUnavailable.Err()
	}
	u.loadedAt = u.now()
	if u.cached != nil && conn.UpdatedAt.Equal(u.cachedAt) {
		return u.cached, nil
	}
	c, err := u.buildClients(conn)
	if err != nil {
		return nil, err
	}
	if old := u.cached; old != nil {
		old.http.Close()
		if old.control != nil {
			// The streams open on the old connection (run events, the outbox) are not cut at once: they end with it a little
			// later and reopen on the new one.
			time.AfterFunc(connectionTTL, func() { _ = old.control.Close() })
		}
	}
	u.cached, u.cachedAt = c, conn.UpdatedAt
	return c, nil
}

func (u *UseCase) buildClients(conn assistantModel.Connection) (*clients, error) {
	key, err := u.cipher.DecryptWithContext(conn.KeyCiphertext, assistantModel.SecretContext(conn.Endpoint, "key"))
	if err != nil {
		return nil, model.ErrPlatform.WithError(err).WithMessage("Failed to open the cyber-brain client key").Err()
	}
	if strings.TrimSpace(conn.CAPEM) == "" {
		return nil, model.ErrPlatform.WithMessage("The stored cyber-brain connection has no pinned CA").Err()
	}
	h, err := brain.NewClient(conn.Endpoint, []byte(conn.CertPEM), key, []byte(conn.CAPEM))
	if err != nil {
		return nil, model.ErrPlatform.WithError(err).WithMessage("The stored cyber-brain connection is unusable").Err()
	}
	out := &clients{http: h}
	grpcEndpoint := conn.GRPCEndpoint
	if grpcEndpoint == "" {
		grpcEndpoint = u.grpcEndpoint()
	}
	if grpcEndpoint != "" {
		if out.control, err = brain.NewControl(grpcEndpoint, []byte(conn.CertPEM), key, []byte(conn.CAPEM)); err != nil {
			h.Close()
			return nil, model.ErrPlatform.WithError(err).WithMessage("The stored cyber-brain gRPC endpoint is unusable").Err()
		}
	}
	return out, nil
}

// RenewBrainCertificate gets a new client certificate for the same key over the current mutual-TLS connection when the
// stored one is past two thirds of its lifetime. It reports whether it renewed. The certificate must name the same
// tenant. Only the leader replica runs it (Maintain).
func (u *UseCase) RenewBrainCertificate(ctx context.Context) (bool, error) {
	if !u.Enabled() {
		return false, nil
	}
	conn, found, err := u.store.Get(ctx, u.endpoint())
	if err != nil {
		return false, model.ErrPlatform.WithError(err).WithMessage("Failed to read the cyber-brain connection").Err()
	}
	if !found || !renewDue(conn, u.now()) {
		return false, nil
	}
	built, err := u.buildClients(conn)
	if err != nil {
		return false, err
	}
	current := built.http
	defer func() {
		current.Close()
		if built.control != nil {
			_ = built.control.Close()
		}
	}()
	// The brain renews a certificate only for the key it was issued to (a stolen certificate cannot be rolled to a new key),
	// so the request is made for the stored key.
	keyPEM, err := u.cipher.DecryptWithContext(conn.KeyCiphertext, assistantModel.SecretContext(conn.Endpoint, "key"))
	if err != nil {
		return false, model.ErrPlatform.WithError(err).WithMessage("Failed to open the cyber-brain client key").Err()
	}
	key := agentcrypto.ClientKey{KeyPEM: string(keyPEM)}
	if key.CSRPEM, err = agentcrypto.CSRForKey(key.KeyPEM); err != nil {
		return false, err
	}
	out, err := current.Renew(ctx, key.CSRPEM)
	if err != nil {
		return false, fmt.Errorf("renew the cyber-brain certificate: %w", err)
	}
	cert, err := checkCertificate(out.CertPEM, key.KeyPEM)
	if err != nil {
		return false, err
	}
	if cert.Tenant != conn.Tenant {
		return false, fmt.Errorf("the renewed cyber-brain certificate names the tenant %q instead of %q", cert.Tenant, conn.Tenant)
	}
	// A renewal may bring a new CA (rotation): the new client certificate must chain to it before it replaces the pin.
	pinned := conn.CAPEM
	if strings.TrimSpace(out.CAPEM) != "" {
		pinned = out.CAPEM
	}
	if err = verifyIssuedBy(out.CertPEM, pinned, u.now()); err != nil {
		return false, err
	}
	sealed, err := u.sealKey(conn.Endpoint, key.KeyPEM)
	if err != nil {
		return false, err
	}
	ok, err := u.store.SetCertificate(ctx, conn.Endpoint, conn.Tenant, out.CertPEM, sealed, pinned, cert.NotAfter, u.now().UTC())
	if err != nil {
		return false, model.ErrPlatform.WithError(err).WithMessage("Failed to save the renewed cyber-brain certificate").Err()
	}
	if ok {
		u.forget()
	}
	return ok, nil
}

// forget drops the loaded client so the next call reads the stored connection.
func (u *UseCase) forget() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.loadedAt = time.Time{}
}

// renewDue says the client certificate is past two thirds of its own lifetime.
func renewDue(conn assistantModel.Connection, now time.Time) bool {
	cert, err := agentcrypto.ReadCertificate(conn.CertPEM)
	if err != nil {
		// An unreadable stored certificate cannot be judged by its own lifetime: renew once it has ended.
		return !now.Before(conn.CertNotAfter)
	}
	renewAt := cert.NotBefore.Add(cert.NotAfter.Sub(cert.NotBefore) * renewAtNumerator / renewAtDenominator)
	return !now.Before(renewAt)
}

// MaintainBrain runs the periodic upkeep until ctx ends: the certificate is renewed when it is due, and the saves
// that wait for a background import are followed. A failure is logged and tried again at the next pass. It is meant for the leader replica only.
func (u *UseCase) MaintainBrain(ctx context.Context) {
	if !u.Enabled() {
		return
	}
	run := func() {
		if renewed, err := u.RenewBrainCertificate(ctx); err != nil {
			log.Error().Err(err).Msg("Cyber-brain certificate renewal failed")
		} else if renewed {
			log.Info().Msg("Cyber-brain client certificate renewed")
		}
	}
	run()
	settle := func() {
		if err := u.SettleSaves(ctx); err != nil {
			log.Error().Err(err).Msg("Following the assistant saves failed")
		}
	}
	push := func() {
		if err := u.PushStatistics(ctx); err != nil {
			log.Error().Err(err).Msg("Pushing the statistics to the brain failed")
		}
	}
	t := time.NewTicker(MaintainInterval)
	defer t.Stop()
	s := time.NewTicker(SettleInterval)
	defer s.Stop()
	p := time.NewTicker(StatsInterval)
	defer p.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		case <-s.C:
			settle()
		case <-p.C:
			push()
		}
	}
}

func orDefault(v, fallback time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return fallback
}
