-- name: GetBrainConnection :one
-- The stored connection to one brain endpoint.
SELECT *
FROM brain_connections
WHERE endpoint = sqlc.arg(endpoint);

-- name: CreateBrainConnection :execrows
-- A freshly enrolled connection. Two replicas enrolling at once: the first row stays (0 rows for the second).
INSERT INTO brain_connections (endpoint, tenant, client_cert_pem, client_key_ciphertext, ca_pem, cert_not_after,
                               created_at, updated_at)
VALUES (sqlc.arg(endpoint), sqlc.arg(tenant), sqlc.arg(client_cert_pem), sqlc.arg(client_key_ciphertext),
        sqlc.arg(ca_pem), sqlc.arg(cert_not_after), sqlc.arg(created_at), sqlc.arg(updated_at))
ON CONFLICT (endpoint) DO NOTHING;

-- name: SetBrainConnectionCertificate :execrows
-- A renewed client certificate with its new key (the tenant is the certificate CN and stays).
UPDATE brain_connections
SET client_cert_pem       = sqlc.arg(client_cert_pem),
    client_key_ciphertext = sqlc.arg(client_key_ciphertext),
    ca_pem                = sqlc.arg(ca_pem),
    cert_not_after        = sqlc.arg(cert_not_after),
    updated_at            = sqlc.arg(updated_at)
WHERE endpoint = sqlc.arg(endpoint)
  AND tenant = sqlc.arg(tenant);
