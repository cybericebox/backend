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
        last_step         = NULL,
        failures          = 0,
        locked_until      = NULL
WHERE user_totp.confirmed_at IS NULL;

-- name: ConfirmUserTOTP :execrows
-- Confirms the setup that holds this very secret (not one started again meanwhile) with the step of its first code.
UPDATE user_totp
SET confirmed_at = sqlc.arg(confirmed_at)::timestamptz,
    last_step    = sqlc.arg(last_step)::bigint,
    failures     = 0,
    locked_until = NULL
WHERE user_id = sqlc.arg(user_id)
  AND confirmed_at IS NULL
  AND secret_ciphertext = sqlc.arg(secret_ciphertext);

-- name: UseUserTOTPStep :execrows
-- Takes the step of an accepted code. No row when that step or a later one was already used: the code is refused.
UPDATE user_totp
SET last_step    = sqlc.arg(step)::bigint,
    failures     = 0,
    locked_until = NULL
WHERE user_id = sqlc.arg(user_id)
  AND confirmed_at IS NOT NULL
  AND (last_step IS NULL OR last_step < sqlc.arg(step)::bigint);

-- name: ReserveUserTOTPAttempt :one
-- Reserves one code check before the code is compared: counted up front, in one statement, so parallel guesses cannot
-- all pass a lockout that is read first and counted later. No row while the checks are locked. The max_failures-th
-- attempt locks the checks for lock_base_secs, doubling with every further attempt up to lock_max_secs; a right code
-- resets both (UseUserTOTPStep, ConfirmUserTOTP).
UPDATE user_totp
SET failures     = failures + 1,
    locked_until = CASE
                       WHEN failures + 1 >= sqlc.arg(max_failures)::integer THEN
                           sqlc.arg(now)::timestamptz + make_interval(secs => LEAST(sqlc.arg(lock_max_secs)::float8,
                               sqlc.arg(lock_base_secs)::float8 * power(2, failures + 1 - sqlc.arg(max_failures)::integer)))
                       ELSE locked_until END
WHERE user_id = sqlc.arg(user_id)
  AND (locked_until IS NULL OR locked_until <= sqlc.arg(now)::timestamptz)
RETURNING failures;

-- name: RefundUserTOTPAttempt :exec
-- Gives a reserved attempt back (a right code of a step already used is no guess); a lock that attempt set goes too.
UPDATE user_totp
SET failures     = GREATEST(failures - 1, 0),
    locked_until = CASE WHEN failures - 1 < sqlc.arg(max_failures)::integer THEN NULL ELSE locked_until END
WHERE user_id = sqlc.arg(user_id);

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
-- Takes an unused code once: no row for an unknown or used code. The hashes are the code under every key of the
-- platform keyring (a rotation keeps the codes working).
UPDATE user_recovery_codes
SET used_at = sqlc.arg(used_at)::timestamptz
WHERE user_id = sqlc.arg(user_id)
  AND code_hash = ANY (sqlc.arg(code_hashes)::bytea[])
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

-- name: ClearUserStepUps :execrows
-- Forgets the step-ups of every session of the person (a factor or the password changed).
UPDATE sessions
SET step_up_at     = NULL,
    step_up_method = NULL
WHERE user_id = $1
  AND step_up_at IS NOT NULL;

-- name: LockUserForFactorChange :one
-- Serializes the factor changes of one person (the last-factor guard and its write are one decision).
SELECT id
FROM users
WHERE id = $1
FOR UPDATE;

-- name: UserTOTPExists :one
SELECT EXISTS (SELECT 1 FROM user_totp) AS present;

-- name: GetSessionStepUp :one
SELECT step_up_at, step_up_method
FROM sessions
WHERE id = $1;
