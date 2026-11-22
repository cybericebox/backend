-- The leader looks for the live test labs of opponent runs that are still there although none of their runs is active (a teardown
-- that was cut short) and destroys them. It reads the live test deploys and probes each for opponent runs: this index answers that
-- probe without scanning the run history (the active runs have assistant_runs_deploy_active).
CREATE INDEX assistant_runs_opponent_deploy ON assistant_runs (deploy_id) WHERE opponent > 0;
