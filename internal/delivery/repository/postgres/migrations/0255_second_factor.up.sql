-- The second factor of an account (access-control design §6.6, plan P7): an authenticator app (TOTP) and the one-time
-- recovery codes, plus the step-up of a session.
--
-- The TOTP secret is sealed with the platform key (PLATFORM_SECRETS_KEY), bound to its person (secretctx.TOTPSecret);
-- a row without confirmed_at is a setup that waits for its first code. last_step is the RFC 6238 time step of the last
-- accepted code: a code of that step or an older one is refused, so a code never works twice. failures counts the
-- wrong codes since the last right one and locked_until stops the checks after too many: kept in the row, so the
-- lockout holds across replicas.
CREATE TABLE user_totp
(
    user_id           uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    secret_ciphertext text        NOT NULL,
    created_at        timestamptz NOT NULL,
    confirmed_at      timestamptz,
    last_step         bigint,
    failures          integer     NOT NULL DEFAULT 0,
    locked_until      timestamptz
);

-- Ten one-time codes per person, stored only as an HMAC with a key derived from the platform key
-- (secondFactorModel.HashRecoveryCode) and shown once. A used code keeps its row (used_at) until the codes are replaced.
CREATE TABLE user_recovery_codes
(
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    code_hash  bytea       NOT NULL,
    created_at timestamptz NOT NULL,
    used_at    timestamptz,
    PRIMARY KEY (user_id, code_hash)
);

-- The step-up of a session: when it was last confirmed and how. Read only by the routes that need a step-up.
ALTER TABLE sessions
    ADD COLUMN step_up_at     timestamptz,
    ADD COLUMN step_up_method text CHECK (step_up_method IN ('passkey', 'totp', 'password', 'recent-sign-in', 'recovery-code'));
