ALTER TABLE assistant_runs
    DROP COLUMN IF EXISTS spec_id;
ALTER TABLE brain_connections
    DROP COLUMN IF EXISTS grpc_endpoint;
