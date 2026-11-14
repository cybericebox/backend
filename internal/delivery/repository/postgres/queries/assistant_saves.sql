-- name: GetAssistantSave :one
-- The task a session was already saved as by this author.
SELECT exercise_id
FROM assistant_saves
WHERE session_id = sqlc.arg(session_id)
  AND author_id = sqlc.arg(author_id);

-- name: CreateAssistantSave :execrows
-- Records the import of a session. A row that is already there stays (0 rows).
INSERT INTO assistant_saves (session_id, author_id, exercise_id, created_at)
VALUES (sqlc.arg(session_id), sqlc.arg(author_id), sqlc.arg(exercise_id), sqlc.arg(created_at))
ON CONFLICT (session_id, author_id) DO NOTHING;
