-- An opponent run: the run of one automated attacker or defender the variant's manifest declares. opponent is the 1-based
-- position in the variant's opponents list (0 for every other run); role is then the opponent's own role. A variant has at
-- most one active run of each opponent.
ALTER TABLE assistant_runs
    ADD COLUMN opponent integer NOT NULL DEFAULT 0 CHECK (opponent >= 0);

CREATE UNIQUE INDEX assistant_runs_one_active_per_opponent ON assistant_runs (variant_id, opponent) WHERE opponent > 0 AND state IN ('starting', 'running', 'closing');
