-- name: ListLabActivityGroups :many
-- The lab groups whose activity the daemon pulls: the groups of the events whose statistics collection is on (final or
-- continuous) while the event runs and for an hour after it ends (the laboratory keeps a short ring).
SELECT DISTINCT lb.lab_group_name
FROM events e
         LEFT JOIN event_assistant_stats m ON m.event_id = e.id
         JOIN lab_bindings lb ON lb.event_id = e.id
WHERE COALESCE(m.mode, sqlc.arg(default_mode)::smallint) >= 2
  AND e.start_at <= sqlc.arg(now)::timestamptz
  AND COALESCE(e.manual_finished_at, e.finish_at, 'infinity'::timestamptz) + interval '1 hour' > sqlc.arg(now)::timestamptz
ORDER BY 1;

-- name: GetLabActivityCursor :one
SELECT cursor, gap
FROM lab_activity_cursors
WHERE lab_group_name = sqlc.arg(lab_group_name);

-- name: ApplyLabActivity :exec
-- Folds one page of a group's activity into the running figures and moves the group's cursor, in one statement, so the page
-- is counted exactly once. batch is {labs:[{lab,egress}], clients:[{lab,client,bytes}], dsts:[{lab,client,dst}],
-- sessions:[{lab,session_id,client,opened_ms,closed_ms}]}, each list holding one entry per key.
WITH cur AS (
    INSERT INTO lab_activity_cursors (lab_group_name, cursor, gap, updated_at)
        VALUES (sqlc.arg(lab_group_name), sqlc.arg(cursor), sqlc.arg(gap), sqlc.arg(now)::timestamptz)
        ON CONFLICT (lab_group_name) DO UPDATE
            SET cursor = EXCLUDED.cursor, gap = lab_activity_cursors.gap OR EXCLUDED.gap, updated_at = EXCLUDED.updated_at
), labs AS (
    INSERT INTO lab_activity (lab_group_name, lab_name, egress_used, updated_at)
        SELECT sqlc.arg(lab_group_name), x ->> 'lab', COALESCE((x ->> 'egress')::boolean, false), sqlc.arg(now)::timestamptz
        FROM jsonb_array_elements(sqlc.arg(batch)::jsonb -> 'labs') AS x
        ON CONFLICT (lab_group_name, lab_name) DO UPDATE
            SET egress_used = lab_activity.egress_used OR EXCLUDED.egress_used, updated_at = EXCLUDED.updated_at
), clients AS (
    INSERT INTO lab_activity_clients (lab_group_name, lab_name, client, bytes)
        SELECT sqlc.arg(lab_group_name), x ->> 'lab', x ->> 'client', (x ->> 'bytes')::bigint
        FROM jsonb_array_elements(sqlc.arg(batch)::jsonb -> 'clients') AS x
        ON CONFLICT (lab_group_name, lab_name, client) DO UPDATE
            SET bytes = lab_activity_clients.bytes + EXCLUDED.bytes
), dsts AS (
    INSERT INTO lab_activity_dsts (lab_group_name, lab_name, client, dst_address)
        SELECT sqlc.arg(lab_group_name), x ->> 'lab', x ->> 'client', x ->> 'dst'
        FROM jsonb_array_elements(sqlc.arg(batch)::jsonb -> 'dsts') AS x
        ON CONFLICT DO NOTHING
), sessions AS (
    INSERT INTO lab_activity_sessions (lab_group_name, lab_name, session_id, client, opened_at, closed_at)
        SELECT sqlc.arg(lab_group_name), x ->> 'lab', x ->> 'session_id', x ->> 'client',
               to_timestamp((x ->> 'opened_ms')::bigint / 1000.0),
               CASE WHEN (x ->> 'closed_ms')::bigint > 0 THEN to_timestamp((x ->> 'closed_ms')::bigint / 1000.0) END
        FROM jsonb_array_elements(sqlc.arg(batch)::jsonb -> 'sessions') AS x
        ON CONFLICT (lab_group_name, lab_name, session_id) DO UPDATE
            SET closed_at = COALESCE(EXCLUDED.closed_at, lab_activity_sessions.closed_at)
)
SELECT 1;

-- name: ListAssistantLabActivity :many
-- The activity of the labs of teams x tasks, per (event, team, event exercise): the labs of one instance (a recreate makes
-- another lab) are folded together. egress_used: any of its labs had egress; vpn_bytes: bytes the team's VPN clients exchanged
-- with the labs; devices_touched: distinct lab addresses reached; console_seconds: console time, a session still open (or one
-- longer than max_session_seconds) counted up to that cap. An instance whose group was never pulled (reports off, or not yet) is not returned: its figures are "not reported", not zero.
WITH inst AS (
    SELECT DISTINCT lb.event_id, lb.event_team_id, ec.event_exercise_id, lb.lab_group_name, lb.lab_name
    FROM lab_bindings lb
             JOIN event_challenges ec ON ec.id = lb.event_challenge_id
    WHERE lb.event_team_id = ANY (sqlc.arg(team_ids)::uuid[])
      AND ec.event_exercise_id = ANY (sqlc.arg(exercise_ids)::uuid[])
), per_lab AS (
    SELECT i.event_id, i.event_team_id, i.event_exercise_id, i.lab_group_name, i.lab_name, COALESCE(a.egress_used, false) AS egress_used,
           (SELECT COALESCE(sum(c.bytes), 0) FROM lab_activity_clients c
            WHERE c.lab_group_name = i.lab_group_name AND c.lab_name = i.lab_name)::bigint AS bytes,
           (SELECT COALESCE(sum(EXTRACT(EPOCH FROM (LEAST(COALESCE(s.closed_at, sqlc.arg(now)::timestamptz),
                                                          s.opened_at + make_interval(secs => sqlc.arg(max_session_seconds)::float8))
               - s.opened_at))), 0) FROM lab_activity_sessions s
            WHERE s.lab_group_name = i.lab_group_name AND s.lab_name = i.lab_name)::float8 AS console_seconds
    FROM inst i
             LEFT JOIN lab_activity a ON a.lab_group_name = i.lab_group_name AND a.lab_name = i.lab_name
    -- A lab the reports do not name is a lab with no activity, once its group is pulled at all (a cursor exists).
    WHERE a.lab_name IS NOT NULL
       OR EXISTS (SELECT 1 FROM lab_activity_cursors k WHERE k.lab_group_name = i.lab_group_name)
), folded AS (
    SELECT event_id, event_team_id, event_exercise_id,
           bool_or(egress_used)::bool AS egress_used, sum(bytes)::bigint AS vpn_bytes, sum(console_seconds)::float8 AS console_seconds
    FROM per_lab
    GROUP BY event_id, event_team_id, event_exercise_id
), touched AS (
    SELECT i.event_team_id, i.event_exercise_id, count(DISTINCT d.dst_address)::bigint AS devices
    FROM inst i
             JOIN lab_activity_dsts d ON d.lab_group_name = i.lab_group_name AND d.lab_name = i.lab_name
    GROUP BY i.event_team_id, i.event_exercise_id
)
SELECT f.event_id, f.event_team_id, f.event_exercise_id, f.egress_used, f.vpn_bytes,
       COALESCE(t.devices, 0)::bigint AS devices_touched, f.console_seconds
FROM folded f
         LEFT JOIN touched t ON t.event_team_id = f.event_team_id AND t.event_exercise_id = f.event_exercise_id;

-- name: GetLabActivityOfClient :one
-- The activity of one lab as one VPN client saw it (a grader run's lab): whether the lab had egress, the bytes the client
-- exchanged with it, the distinct lab addresses it reached and its console time (a session open, or longer than the cap, is
-- counted up to max_session_seconds). No row: the lab has no report.
SELECT a.egress_used::bool                                                                                     AS egress_used,
       (SELECT COALESCE(sum(c.bytes), 0) FROM lab_activity_clients c
        WHERE c.lab_group_name = a.lab_group_name AND c.lab_name = a.lab_name AND c.client = sqlc.arg(client))::bigint AS vpn_bytes,
       (SELECT count(DISTINCT d.dst_address) FROM lab_activity_dsts d
        WHERE d.lab_group_name = a.lab_group_name AND d.lab_name = a.lab_name AND d.client = sqlc.arg(client))::bigint AS devices_touched,
       (SELECT COALESCE(sum(EXTRACT(EPOCH FROM (LEAST(COALESCE(s.closed_at, sqlc.arg(now)::timestamptz),
                                                      s.opened_at + make_interval(secs => sqlc.arg(max_session_seconds)::float8))
           - s.opened_at))), 0) FROM lab_activity_sessions s
        WHERE s.lab_group_name = a.lab_group_name AND s.lab_name = a.lab_name AND s.client = sqlc.arg(client))::float8 AS console_seconds
FROM lab_activity a
WHERE a.lab_group_name = sqlc.arg(lab_group_name)
  AND a.lab_name = sqlc.arg(lab_name);

-- name: PurgeLabActivity :exec
-- Drops the figures of labs nothing needs any more: no event binding names the lab (the event or its lab is gone, or it was a
-- test lab) and nothing was reported for it since the cutoff; then the cursors of groups left without a lab.
WITH gone AS (
    DELETE FROM lab_activity a
        WHERE a.updated_at < sqlc.arg(before)::timestamptz
            AND NOT EXISTS (SELECT 1 FROM lab_bindings lb WHERE lb.lab_group_name = a.lab_group_name AND lb.lab_name = a.lab_name)
        RETURNING a.lab_group_name, a.lab_name
), c AS (
    DELETE FROM lab_activity_clients c USING gone g WHERE c.lab_group_name = g.lab_group_name AND c.lab_name = g.lab_name
), d AS (
    DELETE FROM lab_activity_dsts d USING gone g WHERE d.lab_group_name = g.lab_group_name AND d.lab_name = g.lab_name
), s AS (
    DELETE FROM lab_activity_sessions s USING gone g WHERE s.lab_group_name = g.lab_group_name AND s.lab_name = g.lab_name
)
DELETE FROM lab_activity_cursors k
WHERE k.updated_at < sqlc.arg(before)::timestamptz
  AND NOT EXISTS (SELECT 1 FROM lab_activity a WHERE a.lab_group_name = k.lab_group_name)
  AND NOT EXISTS (SELECT 1 FROM lab_bindings lb WHERE lb.lab_group_name = k.lab_group_name);
