-- The brain's machine control plane moved to gRPC (pkg/brainapi/v1): the stored connection names the gRPC endpoint next to the
-- HTTP one (the chat and the enrollment stay on HTTP; empty: BRAIN_GRPC_ENDPOINT gives it). The outbox is now one bidirectional
-- stream without sequence numbers (the brain re-sends every unanswered request), so its cursor goes.
ALTER TABLE brain_connections
    ADD COLUMN grpc_endpoint text NOT NULL DEFAULT '',
    DROP COLUMN outbox_seq;

-- The GetOpponent spec an opponent run was deployed from; StartRun names it. Empty for a grader, a lab, and an opponent of a
-- brain that has no specs (the BRAIN_TOOLS_IMAGE fallback). Its short-lived pull credential is never stored.
ALTER TABLE assistant_runs
    ADD COLUMN spec_id text NOT NULL DEFAULT '';
