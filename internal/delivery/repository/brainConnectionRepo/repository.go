// Package brainConnectionRepo persists the platform's client connection to cyber-brain: the issued certificate in
// plain, its private key as ciphertext.
package brainConnectionRepo

import (
	"context"
	"time"

	"github.com/cybericebox/daemon/internal/delivery/repository/postgres"
	repositoryTools "github.com/cybericebox/daemon/internal/delivery/repository/tools"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
)

type Queries interface {
	GetBrainConnection(context.Context, string) (postgres.BrainConnection, error)
	CreateBrainConnection(context.Context, postgres.CreateBrainConnectionParams) (int64, error)
	SetBrainConnectionCertificate(context.Context, postgres.SetBrainConnectionCertificateParams) (int64, error)
	GetBrainOutboxSeq(context.Context, string) (int64, error)
	SetBrainOutboxSeq(context.Context, postgres.SetBrainOutboxSeqParams) (int64, error)
}

type Repository struct{ q Queries }

func New(q Queries) *Repository { return &Repository{q: q} }

// Get reads the connection to one endpoint; found is false when none is stored.
func (r *Repository) Get(ctx context.Context, endpoint string) (c assistantModel.Connection, found bool, err error) {
	row, err := r.q.GetBrainConnection(ctx, endpoint)
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return assistantModel.Connection{}, false, nil
		}
		return assistantModel.Connection{}, false, err
	}
	return assistantModel.Connection{
		Endpoint: row.Endpoint, Tenant: row.Tenant, CertPEM: row.ClientCertPem, KeyCiphertext: row.ClientKeyCiphertext,
		CAPEM: row.CaPem, CertNotAfter: row.CertNotAfter, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, true, nil
}

// Create stores a freshly enrolled connection. It reports false when another replica stored one for the endpoint first.
func (r *Repository) Create(ctx context.Context, c assistantModel.Connection) (bool, error) {
	n, err := r.q.CreateBrainConnection(ctx, postgres.CreateBrainConnectionParams{
		Endpoint: c.Endpoint, Tenant: c.Tenant, ClientCertPem: c.CertPEM, ClientKeyCiphertext: c.KeyCiphertext, CaPem: c.CAPEM,
		CertNotAfter: c.CertNotAfter, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	})
	return n > 0, err
}

// SetCertificate stores a renewed certificate with its new key. It reports false when no such connection (endpoint and
// tenant) is stored.
func (r *Repository) SetCertificate(ctx context.Context, endpoint, tenant, certPEM, keyCiphertext, caPEM string, notAfter, at time.Time) (bool, error) {
	n, err := r.q.SetBrainConnectionCertificate(ctx, postgres.SetBrainConnectionCertificateParams{
		ClientCertPem: certPEM, ClientKeyCiphertext: keyCiphertext, CaPem: caPEM, CertNotAfter: notAfter, UpdatedAt: at,
		Endpoint: endpoint, Tenant: tenant,
	})
	return n > 0, err
}

// OutboxSeq is the last outbox request of the brain that was answered (0: none).
func (r *Repository) OutboxSeq(ctx context.Context, endpoint string) (int64, error) {
	seq, err := r.q.GetBrainOutboxSeq(ctx, endpoint)
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return 0, nil
		}
		return 0, err
	}
	return seq, nil
}

// SetOutboxSeq moves the outbox cursor forward; it never goes back.
func (r *Repository) SetOutboxSeq(ctx context.Context, endpoint string, seq int64) error {
	_, err := r.q.SetBrainOutboxSeq(ctx, postgres.SetBrainOutboxSeqParams{OutboxSeq: seq, Endpoint: endpoint})
	return err
}
