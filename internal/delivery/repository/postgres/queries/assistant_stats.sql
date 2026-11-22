-- name: ListAssistantStatsDefaults :many
-- The platform default mode of every source that has one set (the others are off).
SELECT source, mode
FROM assistant_stats_defaults
ORDER BY source;

-- name: UpsertAssistantStatsDefault :execrows
-- Sets the platform default mode of a source.
INSERT INTO assistant_stats_defaults (source, mode, updated_at)
VALUES (sqlc.arg(source), sqlc.arg(mode), sqlc.arg(updated_at))
ON CONFLICT (source) DO UPDATE SET mode = EXCLUDED.mode, updated_at = EXCLUDED.updated_at;

-- name: GetEventAssistantStatsMode :one
-- The event's own mode; no row: it follows the platform default.
SELECT mode
FROM event_assistant_stats
WHERE event_id = sqlc.arg(event_id);

-- name: SetEventAssistantStatsMode :execrows
-- Sets the event's own mode (0 rows: no such event).
INSERT INTO event_assistant_stats (event_id, mode, updated_at)
SELECT e.id, sqlc.arg(mode), sqlc.arg(updated_at)
FROM events e
WHERE e.id = sqlc.arg(event_id)
ON CONFLICT (event_id) DO UPDATE SET mode = EXCLUDED.mode, updated_at = EXCLUDED.updated_at;

-- name: ClearEventAssistantStatsMode :execrows
-- The event follows the platform default again.
DELETE
FROM event_assistant_stats
WHERE event_id = sqlc.arg(event_id);

-- name: ListAssistantStatsEnabledEventTasks :many
-- The tasks (exercise and pinned version) of the events whose collection is on (final or continuous), whether or not
-- they have anything to send yet. exercise_ids empty: all of them.
SELECT DISTINCT ee.exercise_id, ee.exercise_version_id
FROM events e
         LEFT JOIN event_assistant_stats m ON m.event_id = e.id
         JOIN event_exercises ee ON ee.event_id = e.id AND ee.status <> 2
WHERE COALESCE(m.mode, sqlc.arg(default_mode)::smallint) >= 2
  AND (cardinality(sqlc.arg(exercise_ids)::uuid[]) = 0 OR ee.exercise_id = ANY (sqlc.arg(exercise_ids)::uuid[]));

-- name: ListAssistantStatsTeamFacts :many
-- What each engaged team did with each task of the events whose collection is on and due: continuous while the event has
-- started, final once it has ended. One row per (task, team); the team is not in the row, and only ranked teams count
-- (no moderators, no hidden teams, no team with a member in excluded_users: the participants who refused). Times are
-- seconds from the team's first opening of the task to its last solved subtask, NULL when it did not solve all of them.
-- event_id, event_exercise_id and team_id are for the caller only (it turns them into the random per-event team_ref and drops them).
-- wrong_submissions are the team's wrong answers (before its solve, when it solved the task). The lab figures come from the
-- status log of the task's labs (ready_seconds: from the first lab record to the first ready; NULL while never ready;
-- restarts: the highest recreate generation; broken: a lab of the team failed); has_lab is false for a task without a lab.
-- state_checks lists the state-checked subtasks (a state subtask has no flag): their ref and the seconds from the lab being
-- ready to the pass (NULL when it did not pass).
WITH eligible AS (
    SELECT e.id
    FROM events e
             LEFT JOIN event_assistant_stats m ON m.event_id = e.id
    WHERE e.start_at <= sqlc.arg(as_of)::timestamptz
      AND (COALESCE(m.mode, sqlc.arg(default_mode)::smallint) = 3
        OR (COALESCE(m.mode, sqlc.arg(default_mode)::smallint) = 2
            AND ((e.manual_finished_at IS NOT NULL AND e.manual_finished_at <= sqlc.arg(as_of)::timestamptz)
                OR (e.finish_at IS NOT NULL AND e.finish_at <= sqlc.arg(as_of)::timestamptz))))
), teams AS (
    SELECT t.id, t.event_id
    FROM event_teams t
             JOIN eligible el ON el.id = t.event_id
    WHERE NOT t.moderators
      AND event_team_visible(t.hidden, t.moderators, t.event_id, t.individual, t.admitted_manually, t.admission_locked, t.member_count)
      AND NOT EXISTS (SELECT 1
                      FROM event_participants p
                      WHERE p.team_id = t.id
                        AND p.user_id = ANY (sqlc.arg(excluded_users)::uuid[]))
), tasks AS (
    SELECT ee.id AS event_exercise_id, ee.event_id, ee.exercise_id, ee.exercise_version_id,
           count(ec.id) AS subtasks
    FROM event_exercises ee
             JOIN eligible el ON el.id = ee.event_id
             JOIN event_challenges ec ON ec.event_exercise_id = ee.id AND ec.published
    WHERE ee.status <> 2
      AND (cardinality(sqlc.arg(exercise_ids)::uuid[]) = 0 OR ee.exercise_id = ANY (sqlc.arg(exercise_ids)::uuid[]))
    GROUP BY ee.id, ee.event_id, ee.exercise_id, ee.exercise_version_id
), attempts AS (
    SELECT ec.event_exercise_id, a.event_team_id AS team_id, count(*) AS attempts
    FROM effective_challenge_attempts a
             JOIN team_challenges tc ON tc.id = a.team_challenge_id
             JOIN event_challenges ec ON ec.id = tc.event_challenge_id
             JOIN tasks ON tasks.event_exercise_id = ec.event_exercise_id
             JOIN teams ON teams.id = a.event_team_id
    WHERE a.received_at < sqlc.arg(as_of)::timestamptz
    GROUP BY ec.event_exercise_id, a.event_team_id
), solves AS (
    SELECT ec.event_exercise_id, tc.event_team_id AS team_id, count(*) AS solved, max(s.solved_at) AS done_at
    FROM team_challenge_solves s
             JOIN team_challenges tc ON tc.id = s.team_challenge_id
             JOIN event_challenges ec ON ec.id = tc.event_challenge_id AND ec.published
             JOIN tasks ON tasks.event_exercise_id = ec.event_exercise_id
             JOIN teams ON teams.id = tc.event_team_id
    WHERE s.solved_at < sqlc.arg(as_of)::timestamptz
    GROUP BY ec.event_exercise_id, tc.event_team_id
), opens AS (
    SELECT ec.event_exercise_id, o.team_id, min(o.at) AS opened_at
    FROM event_activity o
             JOIN event_challenges ec ON ec.id = o.subject_id
             JOIN tasks ON tasks.event_exercise_id = ec.event_exercise_id
             JOIN teams ON teams.id = o.team_id
    WHERE o.kind = 'task_opened'
      AND o.subject_id IS NOT NULL
      AND o.at < sqlc.arg(as_of)::timestamptz
    GROUP BY ec.event_exercise_id, o.team_id
), wrongs AS (
    SELECT ec.event_exercise_id, a.event_team_id AS team_id, count(*) AS wrongs
    FROM effective_challenge_attempts a
             JOIN team_challenges tc ON tc.id = a.team_challenge_id
             JOIN event_challenges ec ON ec.id = tc.event_challenge_id
             JOIN tasks ON tasks.event_exercise_id = ec.event_exercise_id
             JOIN teams ON teams.id = a.event_team_id
             LEFT JOIN solves so ON so.event_exercise_id = ec.event_exercise_id AND so.team_id = a.event_team_id
    WHERE NOT a.effective_correct
      AND a.received_at < sqlc.arg(as_of)::timestamptz
      AND (so.solved IS NULL OR so.solved < tasks.subtasks OR a.received_at <= so.done_at)
    GROUP BY ec.event_exercise_id, a.event_team_id
), labs AS (
    SELECT ec.event_exercise_id, x.team_id,
           min(x.at)                                  AS started_at,
           min(x.at) FILTER (WHERE x.to_status = 1)   AS ready_at,
           max(x.generation)::bigint                  AS restarts,
           COALESCE(bool_or(x.to_status = 2), false)  AS broken
    FROM event_stand_transitions x
             JOIN event_challenges ec ON ec.id = x.challenge_id
             JOIN tasks ON tasks.event_exercise_id = ec.event_exercise_id
             JOIN teams ON teams.id = x.team_id
    WHERE x.source = 'lab'
      AND x.at < sqlc.arg(as_of)::timestamptz
    GROUP BY ec.event_exercise_id, x.team_id
), hints AS (
    SELECT ec.event_exercise_id, h.event_team_id AS team_id, count(*) AS hints
    FROM team_challenge_hint_unlocks h
             JOIN event_challenges ec ON ec.id = h.event_challenge_id
             JOIN tasks ON tasks.event_exercise_id = ec.event_exercise_id
             JOIN teams ON teams.id = h.event_team_id
    WHERE h.unlocked_at < sqlc.arg(as_of)::timestamptz
    GROUP BY ec.event_exercise_id, h.event_team_id
)
SELECT tasks.exercise_id,
       tasks.exercise_version_id,
       tasks.event_id,
       tasks.event_exercise_id,
       teams.id                                                                    AS team_id,
       tasks.subtasks::bigint                                                      AS subtasks,
       COALESCE(at.attempts, 0)::bigint                                            AS attempts,
       COALESCE(wr.wrongs, 0)::bigint                                              AS wrong_submissions,
       (lb.team_id IS NOT NULL)::bool                                              AS has_lab,
       COALESCE(EXTRACT(EPOCH FROM (lb.ready_at - lb.started_at)), 0)::float8      AS ready_seconds,
       (lb.ready_at IS NOT NULL)::bool                                             AS has_ready_seconds,
       COALESCE(lb.restarts, 0)::bigint                                            AS restarts,
       COALESCE(lb.broken, false)::bool                                            AS broken,
       sc.checks::jsonb                                                            AS state_checks,
       COALESCE(so.solved, 0)::bigint                                              AS solved_subtasks,
       COALESCE(CASE WHEN so.solved = tasks.subtasks AND op.opened_at IS NOT NULL AND op.opened_at <= so.done_at
                         THEN EXTRACT(EPOCH FROM (so.done_at - op.opened_at)) END, 0)::float8 AS solve_seconds,
       COALESCE(so.solved = tasks.subtasks AND op.opened_at IS NOT NULL AND op.opened_at <= so.done_at, false)::bool AS has_solve_seconds,
       COALESCE(hi.hints, 0)::bigint                                               AS hints
FROM tasks
         JOIN teams ON teams.event_id = tasks.event_id
         LEFT JOIN attempts at ON at.event_exercise_id = tasks.event_exercise_id AND at.team_id = teams.id
         LEFT JOIN solves so ON so.event_exercise_id = tasks.event_exercise_id AND so.team_id = teams.id
         LEFT JOIN opens op ON op.event_exercise_id = tasks.event_exercise_id AND op.team_id = teams.id
         LEFT JOIN hints hi ON hi.event_exercise_id = tasks.event_exercise_id AND hi.team_id = teams.id
         LEFT JOIN wrongs wr ON wr.event_exercise_id = tasks.event_exercise_id AND wr.team_id = teams.id
         LEFT JOIN labs lb ON lb.event_exercise_id = tasks.event_exercise_id AND lb.team_id = teams.id
         CROSS JOIN LATERAL (
    SELECT COALESCE(jsonb_agg(jsonb_build_object(
            'subtask_ref', ec.task_id::text,
            'time_to_pass_s', CASE WHEN ts.solved_at IS NOT NULL AND lb.ready_at IS NOT NULL AND ts.solved_at >= lb.ready_at
                                       THEN EXTRACT(EPOCH FROM (ts.solved_at - lb.ready_at)) END
        ) ORDER BY ec.order_index), '[]'::jsonb) AS checks
    FROM event_challenges ec
             JOIN team_challenges tc ON tc.event_challenge_id = ec.id AND tc.event_team_id = teams.id
             LEFT JOIN team_challenge_solves ts ON ts.team_challenge_id = tc.id AND ts.solved_at < sqlc.arg(as_of)::timestamptz
    WHERE ec.event_exercise_id = tasks.event_exercise_id
      AND ec.published
      AND tc.expected_flag = ''
) sc
WHERE at.attempts IS NOT NULL
   OR so.solved IS NOT NULL
   OR op.opened_at IS NOT NULL;

-- name: ListAssistantStatsTaskVersions :many
-- The variants (their topologies name the images) of the versions that are about to be reported.
SELECT id, variants
FROM exercise_versions
WHERE id = ANY (sqlc.arg(version_ids)::uuid[]);

-- name: ListAssistantStatsTeamRefs :many
-- The random ids teams already have in the statistics of these events.
SELECT event_id, team_id, ref
FROM assistant_stats_team_refs
WHERE event_id = ANY (sqlc.arg(event_ids)::uuid[]);

-- name: InsertAssistantStatsTeamRefs :exec
-- Gives teams their id in the statistics of an event. A team that has one already keeps it (a second writer loses).
INSERT INTO assistant_stats_team_refs (event_id, team_id, ref, created_at)
SELECT (x ->> 'event_id')::uuid, (x ->> 'team_id')::uuid, x ->> 'ref', sqlc.arg(now)::timestamptz
FROM jsonb_array_elements(sqlc.arg(refs)::jsonb) AS x
ON CONFLICT (event_id, team_id) DO NOTHING;
