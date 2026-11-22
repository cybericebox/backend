// Package brainConnectionRepo persists the platform's client connection to cyber-brain: the issued certificate in
// plain, its private key as ciphertext.
package brainConnectionRepo

import (
	"context"
	"time"

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	repositoryTools "github.com/cybericebox/backend/internal/delivery/repository/tools"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
)

type Queries interface {
	GetBrainConnection(context.Context, string) (postgres.BrainConnection, error)
	CreateBrainConnection(context.Context, postgres.CreateBrainConnectionParams) (int64, error)
	SetBrainConnectionCertificate(context.Context, postgres.SetBrainConnectionCertificateParams) (int64, error)
	SetBrainConnectionGRPCEndpoint(context.Context, postgres.SetBrainConnectionGRPCEndpointParams) (int64, error)
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
		Endpoint: row.Endpoint, GRPCEndpoint: row.GrpcEndpoint, Tenant: row.Tenant, CertPEM: row.ClientCertPem, KeyCiphertext: row.ClientKeyCiphertext,
		CAPEM: row.CaPem, CertNotAfter: row.CertNotAfter, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, true, nil
}

// Create stores a freshly enrolled connection. It reports false when another replica stored one for the endpoint first.
func (r *Repository) Create(ctx context.Context, c assistantModel.Connection) (bool, error) {
	n, err := r.q.CreateBrainConnection(ctx, postgres.CreateBrainConnectionParams{
		Endpoint: c.Endpoint, GrpcEndpoint: c.GRPCEndpoint, Tenant: c.Tenant, ClientCertPem: c.CertPEM, ClientKeyCiphertext: c.KeyCiphertext, CaPem: c.CAPEM,
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

// SetGRPCEndpoint stores the gRPC endpoint of the control plane of a connection. It reports false when no such connection is
// stored or it names that endpoint already.
func (r *Repository) SetGRPCEndpoint(ctx context.Context, endpoint, grpcEndpoint string, at time.Time) (bool, error) {
	n, err := r.q.SetBrainConnectionGRPCEndpoint(ctx, postgres.SetBrainConnectionGRPCEndpointParams{GrpcEndpoint: grpcEndpoint, UpdatedAt: at, Endpoint: endpoint})
	return n > 0, err
}
