-- name: TouchUserDraftKey :one
-- The live key of this person named by the browser; its last use moves to now. No row: the id is unknown, of
-- another person, or stale.
UPDATE user_draft_keys
SET last_used_at = sqlc.arg(used_at)
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND last_used_at > sqlc.arg(stale_before)
RETURNING key_ciphertext;

-- name: ResealUserDraftKey :execrows
-- Replaces the sealed key with the same key sealed under the current platform key; nothing when the row changed meanwhile.
UPDATE user_draft_keys
SET key_ciphertext = sqlc.arg(key_ciphertext)
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND key_ciphertext = sqlc.arg(old_ciphertext);

-- name: InsertUserDraftKey :exec
INSERT INTO user_draft_keys (id, user_id, key_ciphertext, created_at, last_used_at)
VALUES (sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(key_ciphertext), sqlc.arg(created_at), sqlc.arg(created_at));

-- name: TrimUserDraftKeys :execrows
-- Keeps the keep most recently used keys of the person and deletes the rest.
DELETE
FROM user_draft_keys d
WHERE d.user_id = sqlc.arg(user_id)
  AND d.id NOT IN (SELECT k.id
                 FROM user_draft_keys k
                 WHERE k.user_id = sqlc.arg(user_id)
                 ORDER BY k.last_used_at DESC, k.id
                 LIMIT sqlc.arg(keep)::bigint);

-- name: DeleteUserDraftKey :execrows
DELETE
FROM user_draft_keys
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id);

-- name: DeleteUserDraftKeysExcept :execrows
DELETE
FROM user_draft_keys
WHERE user_id = sqlc.arg(user_id)
  AND id <> sqlc.arg(keep_id);

-- name: DeleteAllUserDraftKeys :execrows
DELETE
FROM user_draft_keys
WHERE user_id = $1;

-- name: DeleteStaleUserDraftKeys :execrows
DELETE
FROM user_draft_keys
WHERE user_id = sqlc.arg(user_id)
  AND last_used_at <= sqlc.arg(stale_before);

-- name: DeleteStaleDraftKeys :execrows
DELETE
FROM user_draft_keys
WHERE last_used_at <= sqlc.arg(stale_before);
