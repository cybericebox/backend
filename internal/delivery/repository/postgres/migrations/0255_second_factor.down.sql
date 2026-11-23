ALTER TABLE sessions
    DROP COLUMN IF EXISTS step_up_method,
    DROP COLUMN IF EXISTS step_up_at;
DROP TABLE IF EXISTS user_recovery_codes;
DROP TABLE IF EXISTS user_totp;
