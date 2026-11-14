DROP INDEX IF EXISTS assistant_runs_one_active_per_opponent;
ALTER TABLE assistant_runs
    DROP COLUMN IF EXISTS opponent;
