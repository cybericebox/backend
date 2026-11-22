DROP INDEX IF EXISTS assistant_runs_deploy_active;
ALTER TABLE assistant_runs
    DROP COLUMN IF EXISTS tools_index;
