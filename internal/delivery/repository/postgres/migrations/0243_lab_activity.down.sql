ALTER TABLE assistant_runs
    DROP COLUMN IF EXISTS lab_activity;

DROP TABLE IF EXISTS lab_activity_sessions;
DROP TABLE IF EXISTS lab_activity_dsts;
DROP TABLE IF EXISTS lab_activity_clients;
DROP TABLE IF EXISTS lab_activity;
DROP TABLE IF EXISTS lab_activity_cursors;
