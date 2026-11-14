-- name: CreateAssistantRun :exec
-- A run that is starting. The partial unique index refuses a second active run of the variant.
INSERT INTO assistant_runs (id, exercise_id, version_id, variant_id, deploy_id, actor_id, role, state, created_at, updated_at, expires_at)
VALUES (sqlc.arg(id), sqlc.arg(exercise_id), sqlc.arg(version_id), sqlc.arg(variant_id), sqlc.arg(deploy_id), sqlc.arg(actor_id), sqlc.arg(role),
        'starting', sqlc.arg(now)::timestamptz, sqlc.arg(now)::timestamptz, sqlc.arg(expires_at)::timestamptz);

-- name: GetAssistantRun :one
SELECT *
FROM assistant_runs
WHERE id = sqlc.arg(id);

-- name: ListAssistantRunsOfVariant :many
-- The runs of a variant, newest first.
SELECT *
FROM assistant_runs
WHERE variant_id = sqlc.arg(variant_id)
ORDER BY created_at DESC
LIMIT sqlc.arg(max_rows);

-- name: ListActiveAssistantRuns :many
-- The runs the leader drives: starting, running or closing.
SELECT *
FROM assistant_runs
WHERE state IN ('starting', 'running', 'closing')
ORDER BY created_at
LIMIT sqlc.arg(max_rows);

-- name: CountActiveAssistantRunsOfActor :one
SELECT count(*)::bigint
FROM assistant_runs
WHERE actor_id = sqlc.arg(actor_id)
  AND state IN ('starting', 'running', 'closing');

-- name: SetAssistantRunAccessClient :execrows
-- The WireGuard client made for the run, recorded before the run is started so a crash leaves a revocable record.
UPDATE assistant_runs
SET access_client = sqlc.arg(access_client),
    updated_at    = sqlc.arg(now)::timestamptz
WHERE id = sqlc.arg(id)
  AND state = 'starting';

-- name: SetAssistantRunRunning :execrows
-- The brain accepted the run.
UPDATE assistant_runs
SET state      = 'running',
    started_at = sqlc.arg(now)::timestamptz,
    updated_at = sqlc.arg(now)::timestamptz
WHERE id = sqlc.arg(id)
  AND state = 'starting';

-- name: CloseAssistantRun :execrows
-- Decides how the run ends; the teardown follows and then FinishAssistantRun. Only the first decision counts.
UPDATE assistant_runs
SET state          = 'closing',
    end_state      = sqlc.arg(end_state),
    failure_reason = sqlc.arg(failure_reason),
    updated_at     = sqlc.arg(now)::timestamptz
WHERE id = sqlc.arg(id)
  AND state IN ('starting', 'running');

-- name: FinishAssistantRun :execrows
-- The teardown is done: the run takes its end state.
UPDATE assistant_runs
SET state       = end_state,
    finished_at = sqlc.arg(now)::timestamptz,
    updated_at  = sqlc.arg(now)::timestamptz
WHERE id = sqlc.arg(id)
  AND state = 'closing';

-- name: SetAssistantRunMeasurement :execrows
-- The final aggregate the brain sent with run_finished.
UPDATE assistant_runs
SET measurement = sqlc.arg(measurement)::jsonb,
    updated_at  = sqlc.arg(now)::timestamptz
WHERE id = sqlc.arg(id);

-- name: SetAssistantRunStreamDown :execrows
-- Since when the event stream cannot be read (a zero time clears it).
UPDATE assistant_runs
SET stream_down_since = sqlc.narg(since)::timestamptz,
    updated_at        = sqlc.arg(now)::timestamptz
WHERE id = sqlc.arg(id);

-- name: AdvanceAssistantRunSeq :execrows
UPDATE assistant_runs
SET last_seq = GREATEST(last_seq, sqlc.arg(seq)::bigint)
WHERE id = sqlc.arg(id);

-- name: InsertAssistantRunEvent :execrows
-- Stores one brain frame verbatim; a frame seen again (a resumed stream) is ignored.
INSERT INTO assistant_run_events (run_id, seq, type, data, created_at)
VALUES (sqlc.arg(run_id), sqlc.arg(seq), sqlc.arg(type), sqlc.arg(data), sqlc.arg(now)::timestamptz)
ON CONFLICT DO NOTHING;

-- name: ListAssistantRunEvents :many
SELECT seq, type, data, created_at
FROM assistant_run_events
WHERE run_id = sqlc.arg(run_id)
  AND seq > sqlc.arg(after)::bigint
ORDER BY seq
LIMIT sqlc.arg(max_rows);

-- name: InsertAssistantRunAnswer :execrows
-- The judgement of one answer event; an answer seen again is ignored (the judgement stays).
INSERT INTO assistant_run_answers (run_id, seq, subtask_ref, correct, judged_at)
VALUES (sqlc.arg(run_id), sqlc.arg(seq), sqlc.arg(subtask_ref), sqlc.arg(correct), sqlc.arg(now)::timestamptz)
ON CONFLICT DO NOTHING;

-- name: GetAssistantRunAnswer :one
SELECT seq, subtask_ref, correct, judged_at, verdict_sent
FROM assistant_run_answers
WHERE run_id = sqlc.arg(run_id)
  AND seq = sqlc.arg(seq)::bigint;

-- name: SetAssistantRunAnswerSent :execrows
UPDATE assistant_run_answers
SET verdict_sent = true
WHERE run_id = sqlc.arg(run_id)
  AND seq = sqlc.arg(seq)::bigint;

-- name: ListAssistantRunAnswers :many
SELECT seq, subtask_ref, correct, judged_at, verdict_sent
FROM assistant_run_answers
WHERE run_id = sqlc.arg(run_id)
ORDER BY seq;

-- name: ListUnsentAssistantRunAnswers :many
-- The judged answers whose verdict the brain was not told yet.
SELECT seq, subtask_ref, correct, judged_at
FROM assistant_run_answers
WHERE run_id = sqlc.arg(run_id)
  AND NOT verdict_sent
ORDER BY seq;

-- name: ListRecentAssistantRunAnswers :many
-- The judged answers of the run since a moment, oldest first (the answer rate limit).
SELECT subtask_ref, judged_at
FROM assistant_run_answers
WHERE run_id = sqlc.arg(run_id)
  AND judged_at > sqlc.arg(since)::timestamptz
ORDER BY judged_at;
