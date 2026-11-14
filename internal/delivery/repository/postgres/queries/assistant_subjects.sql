-- name: GetAssistantSubject :one
-- The user's pseudonym at the brain.
SELECT subject
FROM assistant_subjects
WHERE user_id = sqlc.arg(user_id);

-- name: CreateAssistantSubject :execrows
-- Gives the user a pseudonym on first use. Two requests at once: the first row stays (0 rows for the second).
INSERT INTO assistant_subjects (user_id, subject, created_at)
VALUES (sqlc.arg(user_id), sqlc.arg(subject), sqlc.arg(created_at))
ON CONFLICT DO NOTHING;
