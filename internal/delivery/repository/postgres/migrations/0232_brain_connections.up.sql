-- The platform's client connection to cyber-brain (the external service behind the authoring assistant), one row per
-- brain endpoint. The client certificate is public, its private key is ciphertext sealed with the platform cipher
-- (context bound to the endpoint). tenant is the CN of the issued certificate. The connection is enrolled once with a
-- one-time token and renewed before the certificate ends.
CREATE TABLE brain_connections
(
    endpoint              text        NOT NULL PRIMARY KEY,
    tenant                text        NOT NULL,
    client_cert_pem       text        NOT NULL,
    client_key_ciphertext text        NOT NULL,
    ca_pem                text        NOT NULL DEFAULT '',
    cert_not_after        timestamptz NOT NULL,
    created_at            timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL
);
