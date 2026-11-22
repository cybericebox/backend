-- The leader looks for the test labs of opponent runs that are still there although none of their runs is active (a teardown that
-- was cut short) and destroys them: the runs of a lab are found by their deploy_id.
CREATE INDEX assistant_runs_opponent_deploy ON assistant_runs (deploy_id) WHERE opponent > 0;
