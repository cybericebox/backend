-- name: GetAssistantSave :one
-- The save of this session by this author: its state, the task (empty until done), the import job, whether the brain
-- was told and when it was claimed.
SELECT state, exercise_id, job_id, notified, claimed_at, failure_code
FROM assistant_saves
WHERE session_id = sqlc.arg(session_id)
  AND author_id = sqlc.arg(author_id);

-- name: ClaimAssistantSave :execrows
-- Claims the save for one request: a new row, a failed one, or a pending one without an import job whose claim is older
-- than the lease (its request died). 0 rows: another request holds it.
INSERT INTO assistant_saves (session_id, author_id, state, archive_hash, claimed_at, created_at)
VALUES (sqlc.arg(session_id), sqlc.arg(author_id), 'pending', sqlc.arg(archive_hash), sqlc.arg(now)::timestamptz, sqlc.arg(now)::timestamptz)
ON CONFLICT (session_id, author_id) DO UPDATE
    SET state        = 'pending',
        archive_hash = EXCLUDED.archive_hash,
        claimed_at   = EXCLUDED.claimed_at,
        exercise_id  = NULL,
        job_id       = NULL,
        notified     = false,
        failure_code = 0
WHERE assistant_saves.state = 'failed'
   OR (assistant_saves.state = 'pending' AND assistant_saves.job_id IS NULL AND assistant_saves.claimed_at < sqlc.arg(stale_before)::timestamptz);

-- name: SetAssistantSaveJob :execrows
-- The claim's background import was queued.
UPDATE assistant_saves
SET job_id = sqlc.arg(job_id)::uuid
WHERE session_id = sqlc.arg(session_id)
  AND author_id = sqlc.arg(author_id)
  AND state = 'pending';

-- name: CompleteAssistantSave :execrows
-- The task exists: the save is done.
UPDATE assistant_saves
SET state       = 'done',
    exercise_id = sqlc.arg(exercise_id)::uuid
WHERE session_id = sqlc.arg(session_id)
  AND author_id = sqlc.arg(author_id)
  AND state = 'pending';

-- name: FailAssistantSave :execrows
-- The import failed with this error code: the next save claims the row again, the leader tells the brain.
UPDATE assistant_saves
SET state        = 'failed',
    failure_code = sqlc.arg(failure_code)
WHERE session_id = sqlc.arg(session_id)
  AND author_id = sqlc.arg(author_id)
  AND state = 'pending';

-- name: SetAssistantSaveNotified :execrows
-- The brain was told the task.
UPDATE assistant_saves
SET notified = true
WHERE session_id = sqlc.arg(session_id)
  AND author_id = sqlc.arg(author_id);

-- name: ListUnsettledAssistantSaves :many
-- The saves the leader still has to finish: a queued import to follow, or an outcome (task or failure) the brain was not
-- told yet. Rows older than the cutoff are left alone.
SELECT session_id, author_id, state, exercise_id, job_id, notified, claimed_at, failure_code
FROM assistant_saves
WHERE ((state = 'pending' AND job_id IS NOT NULL) OR (state IN ('done', 'failed') AND NOT notified))
  AND created_at > sqlc.arg(since)
ORDER BY created_at
LIMIT sqlc.arg(max_rows);
