ALTER TABLE assistant_runs
    DROP COLUMN IF EXISTS spec_id;
ALTER TABLE brain_connections
    DROP COLUMN IF EXISTS grpc_endpoint,
    ADD COLUMN IF NOT EXISTS outbox_seq bigint NOT NULL DEFAULT 0;
