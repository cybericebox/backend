-- name: GetAssistantSave :one
-- The save of this session by this author: the task (empty while its background import runs), the import job and whether
-- the brain was told.
SELECT exercise_id, job_id, notified
FROM assistant_saves
WHERE session_id = sqlc.arg(session_id)
  AND author_id = sqlc.arg(author_id);

-- name: CreateAssistantSave :execrows
-- Records a save: the imported task, or the queued import job. A row that is already there stays (0 rows).
INSERT INTO assistant_saves (session_id, author_id, exercise_id, job_id, created_at)
VALUES (sqlc.arg(session_id), sqlc.arg(author_id), sqlc.narg(exercise_id), sqlc.narg(job_id), sqlc.arg(created_at))
ON CONFLICT (session_id, author_id) DO NOTHING;

-- name: SetAssistantSaveExercise :execrows
-- The background import finished: the task it created.
UPDATE assistant_saves
SET exercise_id = sqlc.arg(exercise_id)
WHERE session_id = sqlc.arg(session_id)
  AND author_id = sqlc.arg(author_id)
  AND exercise_id IS NULL;

-- name: SetAssistantSaveNotified :execrows
-- The brain was told the task.
UPDATE assistant_saves
SET notified = true
WHERE session_id = sqlc.arg(session_id)
  AND author_id = sqlc.arg(author_id);

-- name: DeleteAssistantSave :execrows
-- A failed import leaves nothing behind: the next save starts again.
DELETE
FROM assistant_saves
WHERE session_id = sqlc.arg(session_id)
  AND author_id = sqlc.arg(author_id);

-- name: ListUnsettledAssistantSaves :many
-- The saves the leader still has to finish: an import job to follow, or a task the brain was not told yet. Rows older
-- than the cutoff are left alone.
SELECT session_id, author_id, exercise_id, job_id, notified
FROM assistant_saves
WHERE (exercise_id IS NULL OR NOT notified)
  AND created_at > sqlc.arg(since)
ORDER BY created_at
LIMIT sqlc.arg(max_rows);
