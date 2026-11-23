-- name: GetUserTOTP :one
SELECT *
FROM user_totp
WHERE user_id = $1;

-- name: StartUserTOTP :execrows
-- Stores a new setup that waits for its first code, replacing an older unconfirmed one. No row when the person already
-- has a confirmed authenticator app.
INSERT INTO user_totp (user_id, secret_ciphertext, created_at)
VALUES (sqlc.arg(user_id), sqlc.arg(secret_ciphertext), sqlc.arg(created_at))
ON CONFLICT (user_id) DO UPDATE
    SET secret_ciphertext = EXCLUDED.secret_ciphertext,
        created_at        = EXCLUDED.created_at,
        confirmed_at      = NULL,
        last_step         = NULL
WHERE user_totp.confirmed_at IS NULL;

-- name: ConfirmUserTOTP :execrows
-- Confirms the setup that holds this very secret (not one started again meanwhile) with the step of its first code.
UPDATE user_totp
SET confirmed_at = sqlc.arg(confirmed_at)::timestamptz,
    last_step    = sqlc.arg(last_step)::bigint
WHERE user_id = sqlc.arg(user_id)
  AND confirmed_at IS NULL
  AND secret_ciphertext = sqlc.arg(secret_ciphertext);

-- name: UseUserTOTPStep :execrows
-- Takes the step of an accepted code. No row when that step or a later one was already used: the code is refused.
UPDATE user_totp
SET last_step = sqlc.arg(step)::bigint
WHERE user_id = sqlc.arg(user_id)
  AND confirmed_at IS NOT NULL
  AND (last_step IS NULL OR last_step < sqlc.arg(step)::bigint);

-- name: ResealUserTOTP :execrows
-- The same secret sealed under the current platform key; nothing when the row changed meanwhile.
UPDATE user_totp
SET secret_ciphertext = sqlc.arg(secret_ciphertext)
WHERE user_id = sqlc.arg(user_id)
  AND secret_ciphertext = sqlc.arg(old_ciphertext);

-- name: DeleteUserTOTP :execrows
DELETE
FROM user_totp
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.arg(confirmed_only)::boolean = false OR confirmed_at IS NOT NULL);

-- name: ReplaceUserRecoveryCodes :execrows
-- The new codes replace every old one, used or not, in one statement.
WITH gone AS (DELETE FROM user_recovery_codes WHERE user_recovery_codes.user_id = sqlc.arg(user_id))
INSERT
INTO user_recovery_codes (user_id, code_hash, created_at)
SELECT sqlc.arg(user_id), h, sqlc.arg(created_at)
FROM unnest(sqlc.arg(code_hashes)::bytea[]) AS h;

-- name: UseUserRecoveryCode :execrows
-- Takes an unused code once: no row for an unknown or used code.
UPDATE user_recovery_codes
SET used_at = sqlc.arg(used_at)::timestamptz
WHERE user_id = sqlc.arg(user_id)
  AND code_hash = sqlc.arg(code_hash)
  AND used_at IS NULL;

-- name: CountUnusedUserRecoveryCodes :one
SELECT count(*)
FROM user_recovery_codes
WHERE user_id = $1
  AND used_at IS NULL;

-- name: DeleteUserRecoveryCodes :execrows
DELETE
FROM user_recovery_codes
WHERE user_id = $1;

-- name: CountUserFactorPasskeys :one
-- The passkeys that count as a second factor: registered with user verification and not marked compromised.
SELECT count(*)
FROM user_passkeys
WHERE user_id = $1
  AND user_verified
  AND compromised_at IS NULL;

-- name: SetSessionStepUp :execrows
UPDATE sessions
SET step_up_at     = sqlc.arg(step_up_at)::timestamptz,
    step_up_method = sqlc.arg(step_up_method)::text
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id);

-- name: GetSessionStepUp :one
SELECT step_up_at, step_up_method
FROM sessions
WHERE id = $1;
