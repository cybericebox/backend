-- The last request of the brain's outbox (`GET /v1/requests/watch`) the platform answered: the leader resumes after it
-- after a restart or a change of leader.
ALTER TABLE brain_connections
    ADD COLUMN outbox_seq bigint NOT NULL DEFAULT 0;
