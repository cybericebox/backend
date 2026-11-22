ALTER TABLE assistant_saves
    DROP CONSTRAINT assistant_saves_done_has_task,
    DROP CONSTRAINT assistant_saves_state_check,
    DROP COLUMN claimed_at,
    DROP COLUMN archive_hash,
    DROP COLUMN state;
