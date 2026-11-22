// Package assistant is the domain of the authoring assistant: the platform's connection to cyber-brain (the
// external service behind the authoring chat) and the typed errors of its routes.
package assistant

import "time"

// Connection is the stored client connection to one cyber-brain endpoint: the issued client certificate (public),
// the matching private key as ciphertext, and the server CA the brain is verified with (empty: system roots).
type Connection struct {
	// Endpoint is host:port of the brain's HTTP endpoint (the chat, the enrollment and the renewal), the key of the record.
	Endpoint string
	// GRPCEndpoint is host:port of the brain's gRPC control plane (runs, outbox, statistics, archives, opponents); empty: the
	// configuration's (BRAIN_GRPC_ENDPOINT).
	GRPCEndpoint string
	// Tenant is the CN of the issued certificate: the platform's tenant name at the brain.
	Tenant        string
	CertPEM       string
	KeyCiphertext string
	CAPEM         string
	CertNotAfter  time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Enrollment is what the brain returns for a certificate request.
type Enrollment struct {
	CertPEM string
	CAPEM   string
}

// SecretContext binds the ciphertext of the private key of a connection to its endpoint: a value copied to
// another row fails to open. field is "key".
func SecretContext(endpoint, field string) []byte {
	return []byte("assistant.brain:" + endpoint + ":" + field)
}
