-- A save is claimed first (state pending, with the content hash of the archive and the claim time) and completed when
-- its task exists (state done) or its import failed (state failed, the next save claims the row again). claimed_at is the
-- lease of a claim that has no import job: a claim older than the lease belongs to a request that died.
ALTER TABLE assistant_saves
    ADD COLUMN state        text        NOT NULL DEFAULT 'done',
    ADD COLUMN archive_hash text        NOT NULL DEFAULT '',
    ADD COLUMN claimed_at   timestamptz;

UPDATE assistant_saves
SET state      = CASE WHEN exercise_id IS NULL THEN 'pending' ELSE 'done' END,
    claimed_at = created_at;

ALTER TABLE assistant_saves
    ADD CONSTRAINT assistant_saves_state_check CHECK (state IN ('pending', 'done', 'failed')),
    ADD CONSTRAINT assistant_saves_done_has_task CHECK (state <> 'done' OR exercise_id IS NOT NULL);
