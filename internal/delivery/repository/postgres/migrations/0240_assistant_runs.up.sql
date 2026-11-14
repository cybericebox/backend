-- A run the platform asks cyber-brain to perform against a test lab. Today the only role is the grader, which measures
-- a task by working it the way a participant would; opponents will use the same table. The platform owns the lab and its
-- teardown, the brain only streams events back.
--
-- State: starting (the lab is being deployed, then the run is started at the brain) -> running (events are read) ->
-- closing (the outcome is decided, the lab and the access are being torn down) -> finished | failed | stopped. A run that
-- is starting, running or closing is active; a variant has at most one active run.
CREATE TABLE assistant_runs
(
    id                uuid PRIMARY KEY,
    exercise_id       uuid        NOT NULL,
    version_id        uuid        NOT NULL,
    variant_id        uuid        NOT NULL,
    -- the test deploy (the lab) the run works against; owned by actor_id
    deploy_id         uuid        NOT NULL,
    actor_id          uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role              text        NOT NULL,
    state             text        NOT NULL CHECK (state IN ('starting', 'running', 'closing', 'finished', 'failed', 'stopped')),
    -- how a closing run ends: the state it takes once its teardown is done
    end_state         text        NOT NULL DEFAULT '' CHECK (end_state IN ('', 'finished', 'failed', 'stopped')),
    failure_reason    text        NOT NULL DEFAULT '',
    -- the WireGuard client made for this run (revoked at teardown); empty until it exists
    access_client     text        NOT NULL DEFAULT '',
    -- the last brain event sequence number that was stored
    last_seq          bigint      NOT NULL DEFAULT 0,
    -- since when the brain's event stream cannot be read; null while it is
    stream_down_since timestamptz,
    -- the final aggregate event of the run, opaque
    measurement       jsonb,
    created_at        timestamptz NOT NULL,
    updated_at        timestamptz NOT NULL,
    started_at        timestamptz,
    finished_at       timestamptz,
    -- the longest the run may last; then it is torn down as failed
    expires_at        timestamptz NOT NULL
);

CREATE UNIQUE INDEX assistant_runs_one_active_per_variant ON assistant_runs (variant_id) WHERE state IN ('starting', 'running', 'closing');
CREATE INDEX assistant_runs_variant ON assistant_runs (variant_id, created_at DESC);
CREATE INDEX assistant_runs_actor_active ON assistant_runs (actor_id) WHERE state IN ('starting', 'running', 'closing');

-- The brain's events of a run, stored verbatim with their sequence number (the page reads them from here).
CREATE TABLE assistant_run_events
(
    run_id     uuid        NOT NULL REFERENCES assistant_runs (id) ON DELETE CASCADE,
    seq        bigint      NOT NULL,
    type       text        NOT NULL,
    data       text        NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (run_id, seq)
);

-- The answers a run submitted and how the platform judged them. The submitted value is not kept: only whether it was
-- right. verdict_sent says the brain was told.
CREATE TABLE assistant_run_answers
(
    run_id       uuid        NOT NULL REFERENCES assistant_runs (id) ON DELETE CASCADE,
    seq          bigint      NOT NULL,
    subtask_ref  text        NOT NULL,
    correct      boolean     NOT NULL,
    judged_at    timestamptz NOT NULL,
    verdict_sent boolean     NOT NULL DEFAULT false,
    PRIMARY KEY (run_id, seq)
);
