-- The lab-side figures of the statistics sent to cyber-brain.
--
--   assistant_stats_team_refs  the opaque id a team has in the statistics of ONE event: random per team per event, the same
--                              across the tasks of that event (so the brain gets the event's team x task grid), new for every
--                              event and never derived from the team id. The mapping is kept only to emit it consistently; it
--                              goes with the event or the team.
--   assistant_runs.lab_ready_at  when the test lab of a run was first seen ready (the lab_facts of a grader run report the time
--                              it took).
CREATE TABLE assistant_stats_team_refs
(
    event_id   uuid        NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    team_id    uuid        NOT NULL REFERENCES event_teams (id) ON DELETE CASCADE,
    ref        text        NOT NULL CHECK (ref <> ''),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (event_id, team_id),
    UNIQUE (ref)
);

ALTER TABLE assistant_runs
    ADD COLUMN lab_ready_at timestamptz NULL;
