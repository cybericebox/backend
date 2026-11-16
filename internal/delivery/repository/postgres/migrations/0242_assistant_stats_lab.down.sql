ALTER TABLE assistant_runs
    DROP COLUMN IF EXISTS lab_ready_at;

DROP TABLE IF EXISTS assistant_stats_team_refs;
