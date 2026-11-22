-- name: FreezeEventLabRevealBarrier :one
-- An empty eligible roster is deferred, not an immutable prepared cohort. Until the tasks are shown (the barrier is
-- opened) the cohort is recomputed on every call: a team that becomes eligible, before or after the start, joins it, and a
-- team declined for lack of room (event_lab_reveal_declined, in the same revision and mode) stays out; once opened
-- it is frozen.
WITH candidate AS (
 SELECT ee.event_id,ee.id AS event_exercise_id,ee.revision,ec.task_reveal_mode AS mode,
 COALESCE((SELECT array_agg(team.id ORDER BY team.id) FROM event_teams team WHERE team.event_id=ee.event_id
  AND NOT team.moderators AND NOT EXISTS(SELECT 1 FROM event_lab_reveal_declined d WHERE d.event_exercise_id=ee.id AND d.event_team_id=team.id AND d.revision=ee.revision AND d.mode=ec.task_reveal_mode) AND event_team_admitted(team.event_id,team.individual,team.admitted_manually,team.admission_locked,team.member_count) AND event_team_stand_wanted(team.event_id,team.formed_at)), '{}'::uuid[]) AS eligible_team_ids
 FROM event_exercises ee JOIN event_configs ec ON ec.event_id=ee.event_id
 WHERE ee.id=sqlc.arg(event_exercise_id) AND ee.event_id=sqlc.arg(event_id) AND ee.status=0
), frozen AS (
 INSERT INTO event_lab_reveal_barriers(event_id,event_exercise_id,revision,mode,eligible_team_ids,created_at)
 SELECT event_id,event_exercise_id,revision,mode,eligible_team_ids,sqlc.arg(now) FROM candidate
 WHERE cardinality(eligible_team_ids)>0
 ON CONFLICT(event_exercise_id) DO UPDATE SET
 revision=EXCLUDED.revision,mode=EXCLUDED.mode,
 eligible_team_ids=CASE WHEN event_lab_reveal_barriers.revision<>EXCLUDED.revision OR event_lab_reveal_barriers.mode<>EXCLUDED.mode OR (event_lab_reveal_barriers.opened_at IS NULL AND cardinality(event_lab_reveal_barriers.eligible_team_ids)=0)
  OR event_lab_reveal_barriers.opened_at IS NULL THEN EXCLUDED.eligible_team_ids ELSE event_lab_reveal_barriers.eligible_team_ids END,
 opened_at=CASE WHEN event_lab_reveal_barriers.revision<>EXCLUDED.revision OR event_lab_reveal_barriers.mode<>EXCLUDED.mode THEN NULL ELSE event_lab_reveal_barriers.opened_at END,
 created_at=CASE WHEN event_lab_reveal_barriers.revision<>EXCLUDED.revision OR event_lab_reveal_barriers.mode<>EXCLUDED.mode OR (event_lab_reveal_barriers.opened_at IS NULL AND cardinality(event_lab_reveal_barriers.eligible_team_ids)=0) THEN EXCLUDED.created_at ELSE event_lab_reveal_barriers.created_at END
 RETURNING eligible_team_ids
)
SELECT eligible_team_ids FROM frozen
UNION ALL
SELECT b.eligible_team_ids FROM candidate c JOIN event_lab_reveal_barriers b ON b.event_exercise_id=c.event_exercise_id
 WHERE cardinality(c.eligible_team_ids)=0 AND b.revision=c.revision AND b.mode=c.mode
UNION ALL
SELECT eligible_team_ids FROM candidate WHERE cardinality(eligible_team_ids)=0
 AND NOT EXISTS(SELECT 1 FROM event_lab_reveal_barriers b WHERE b.event_exercise_id=candidate.event_exercise_id);

-- name: ListRetryableEmptyEventLabRevealBarriers :many
SELECT b.event_exercise_id FROM event_lab_reveal_barriers b
JOIN event_exercises ee ON ee.id=b.event_exercise_id AND ee.event_id=b.event_id
JOIN event_configs cfg ON cfg.event_id=b.event_id
WHERE b.event_id=sqlc.arg(event_id) AND b.opened_at IS NULL AND cardinality(b.eligible_team_ids)=0
 AND ee.status=0 AND b.revision=ee.revision AND b.mode=cfg.task_reveal_mode
ORDER BY b.event_exercise_id;

-- name: OpenReadyEventLabRevealBarriers :execrows
UPDATE event_lab_reveal_barriers barrier SET opened_at=sqlc.arg(now)
WHERE barrier.event_id=sqlc.arg(event_id) AND barrier.opened_at IS NULL
AND cardinality(barrier.eligible_team_ids)>0
AND EXISTS(SELECT 1 FROM event_exercises ee JOIN event_configs cfg ON cfg.event_id=ee.event_id WHERE ee.id=barrier.event_exercise_id AND ee.revision=barrier.revision AND ee.status=0 AND cfg.task_reveal_mode=barrier.mode)
AND NOT EXISTS (
 SELECT 1 FROM unnest(barrier.eligible_team_ids) team(id)
 WHERE NOT EXISTS(SELECT 1 FROM event_team_labs l WHERE l.event_team_id=team.id AND l.event_exercise_id=barrier.event_exercise_id
  AND l.materialized AND l.agent_uid<>'' AND l.agent_generation>0 AND l.desired_state='Running' AND l.logical_closed_at IS NULL
  AND l.actual_state='Running' AND l.runtime_ready
  AND l.definition_version_id=(SELECT ee.exercise_version_id FROM event_exercises ee WHERE ee.id=barrier.event_exercise_id)
  AND EXISTS(SELECT 1 FROM lab_bindings b WHERE b.lab_id=l.id AND b.generation=l.generation AND b.lab_group_name=l.lab_group_name AND b.lab_name=l.lab_name)
  AND l.observed_revision=l.desired_revision
  AND NOT EXISTS(SELECT 1 FROM lab_bindings b WHERE b.lab_id=l.id AND b.readiness<>1))
);

-- name: OpenTimedOutEventLabRevealBarriers :many
-- The reveal timeout of an all_ready barrier that still waits: timeout_seconds after it would have opened (the latest
-- of the event start, the opening of the task's stage and the freeze of the cohort) it opens anyway. Opening it shows
-- the tasks only to the teams whose labs are Ready: PublishAvailableTeamChallenges still checks each team's own lab, so
-- a stuck team gets them on its own once its lab is Ready. Each barrier opened here is returned once, with the teams
-- of its cohort that were not Ready (the organizers' alarm names them) and the task name.
WITH due AS (
 SELECT barrier.event_exercise_id FROM event_lab_reveal_barriers barrier
 JOIN event_exercises ee ON ee.id=barrier.event_exercise_id AND ee.event_id=barrier.event_id
 JOIN event_configs cfg ON cfg.event_id=ee.event_id
 JOIN events e ON e.id=barrier.event_id
 LEFT JOIN event_stages stage ON stage.id=ee.stage_id
 WHERE barrier.event_id=sqlc.arg(event_id) AND barrier.opened_at IS NULL AND cardinality(barrier.eligible_team_ids)>0
  AND barrier.mode='all_ready' AND cfg.task_reveal_mode=barrier.mode AND ee.status=0 AND ee.revision=barrier.revision
  AND e.lifecycle_configured
  AND GREATEST(e.start_at, stage.opens_at, barrier.created_at) + make_interval(secs => sqlc.arg(timeout_seconds)::double precision) <= sqlc.arg(now)::timestamptz
 FOR UPDATE OF barrier
), opened AS (
 UPDATE event_lab_reveal_barriers barrier SET opened_at=sqlc.arg(now)::timestamptz
 FROM due WHERE barrier.event_exercise_id=due.event_exercise_id AND barrier.opened_at IS NULL
 RETURNING barrier.event_exercise_id, barrier.revision, barrier.eligible_team_ids
)
SELECT opened.event_exercise_id, opened.revision::bigint AS revision,
 COALESCE((SELECT v.name FROM exercise_versions v WHERE v.id=ee.exercise_version_id), '')::text AS task_name,
 COALESCE(ARRAY(SELECT team.id FROM unnest(opened.eligible_team_ids) team(id) JOIN event_teams t ON t.id=team.id
  WHERE NOT EXISTS(SELECT 1 FROM event_team_labs l WHERE l.event_team_id=team.id AND l.event_exercise_id=opened.event_exercise_id
   AND l.materialized AND l.agent_uid<>'' AND l.agent_generation>0 AND l.desired_state='Running' AND l.logical_closed_at IS NULL
   AND l.actual_state='Running' AND l.runtime_ready
   AND l.definition_version_id=ee.exercise_version_id
   AND EXISTS(SELECT 1 FROM lab_bindings b WHERE b.lab_id=l.id AND b.generation=l.generation AND b.lab_group_name=l.lab_group_name AND b.lab_name=l.lab_name)
   AND l.observed_revision=l.desired_revision
   AND NOT EXISTS(SELECT 1 FROM lab_bindings b WHERE b.lab_id=l.id AND b.readiness<>1))
  ORDER BY t.name, t.id), '{}')::uuid[] AS stuck_team_ids,
 COALESCE(ARRAY(SELECT t.name FROM unnest(opened.eligible_team_ids) team(id) JOIN event_teams t ON t.id=team.id
  WHERE NOT EXISTS(SELECT 1 FROM event_team_labs l WHERE l.event_team_id=team.id AND l.event_exercise_id=opened.event_exercise_id
   AND l.materialized AND l.agent_uid<>'' AND l.agent_generation>0 AND l.desired_state='Running' AND l.logical_closed_at IS NULL
   AND l.actual_state='Running' AND l.runtime_ready
   AND l.definition_version_id=ee.exercise_version_id
   AND EXISTS(SELECT 1 FROM lab_bindings b WHERE b.lab_id=l.id AND b.generation=l.generation AND b.lab_group_name=l.lab_group_name AND b.lab_name=l.lab_name)
   AND l.observed_revision=l.desired_revision
   AND NOT EXISTS(SELECT 1 FROM lab_bindings b WHERE b.lab_id=l.id AND b.readiness<>1))
  ORDER BY t.name, t.id), '{}')::text[] AS stuck_team_names
FROM opened JOIN event_exercises ee ON ee.id=opened.event_exercise_id
ORDER BY opened.event_exercise_id;

-- name: IsEventLabManualReachable :one
SELECT EXISTS(SELECT 1 FROM event_lab_objectives o
 JOIN event_team_labs l ON l.id=o.lab_id
 JOIN team_challenges tc ON tc.event_team_id=l.event_team_id AND tc.event_challenge_id=o.event_challenge_id
 JOIN event_challenges ec ON ec.id=o.event_challenge_id AND ec.published
 JOIN event_exercises ee ON ee.id=ec.event_exercise_id AND ee.status<>2
 LEFT JOIN event_stages stage ON stage.id=ee.stage_id
 WHERE l.id=sqlc.arg(lab_id) AND tc.readiness=2 AND tc.variant_index=l.variant_index
 AND EXISTS(SELECT 1 FROM lab_bindings b WHERE b.lab_id=l.id AND b.event_team_id=l.event_team_id AND b.event_challenge_id=o.event_challenge_id AND b.generation=l.generation AND b.lab_group_name=l.lab_group_name AND b.lab_name=l.lab_name)
 AND event_stage_phase(stage.opens_at,stage.closes_at,stage.returnable,sqlc.arg(now)::timestamptz) IN (1,2)
 AND NOT EXISTS(SELECT 1 FROM event_challenge_prerequisites prerequisite WHERE prerequisite.challenge_id=tc.event_challenge_id
  AND NOT EXISTS(SELECT 1 FROM team_challenges own JOIN team_challenge_solves solved ON solved.team_challenge_id=own.id
   WHERE own.event_team_id=l.event_team_id AND own.event_challenge_id=prerequisite.prerequisite_challenge_id))) AS reachable;

-- name: GetEventLabRevealBarrier :one
SELECT * FROM event_lab_reveal_barriers WHERE event_id=sqlc.arg(event_id) AND event_exercise_id=sqlc.arg(event_exercise_id);
-- name: ListRevealSetLabs :many
SELECT l.* FROM event_team_labs l
WHERE l.event_id=sqlc.arg(event_id) AND l.event_exercise_id=sqlc.arg(event_exercise_id)
 AND l.event_team_id=ANY(sqlc.arg(team_ids)::uuid[])
 AND EXISTS(SELECT 1 FROM lab_bindings b WHERE b.lab_id=l.id AND b.generation=l.generation)
ORDER BY l.event_team_id,l.id;

-- name: InvalidateEventLabRevealBarrier :exec
-- Authorized edits invalidate a different preparation tuple atomically (and the declines made in the old one). They
-- never insert/freeze a cohort before the assignment preparation boundary.
WITH gone AS (
    DELETE FROM event_lab_reveal_barriers b
    USING event_exercises ee,event_configs cfg
    WHERE b.event_exercise_id=ee.id AND cfg.event_id=ee.event_id
     AND ee.event_id=sqlc.arg(event_id) AND ee.id=sqlc.arg(event_exercise_id)
     AND (b.revision<>ee.revision OR b.mode<>cfg.task_reveal_mode)
    RETURNING b.event_exercise_id
)
DELETE FROM event_lab_reveal_declined d
USING event_exercises ee,event_configs cfg
WHERE d.event_exercise_id=ee.id AND cfg.event_id=ee.event_id
 AND ee.event_id=sqlc.arg(event_id) AND ee.id=sqlc.arg(event_exercise_id)
 AND (d.revision<>ee.revision OR d.mode<>cfg.task_reveal_mode);

-- name: DeclineEventLabRevealTeam :exec
-- Keeps a team out of a task's cohort for lack of room: recorded for the task's current revision and reveal mode, and
-- removed from the unopened barrier at once.
WITH declined AS (
    INSERT INTO event_lab_reveal_declined (event_exercise_id, event_team_id, reason, declined_at, revision, mode)
    SELECT ee.id, sqlc.arg(team_id)::uuid, sqlc.arg(reason)::text, sqlc.arg(now), ee.revision, cfg.task_reveal_mode
    FROM event_exercises ee
             JOIN event_configs cfg ON cfg.event_id = ee.event_id
    WHERE ee.event_id = sqlc.arg(event_id) AND ee.id = sqlc.arg(event_exercise_id)
    ON CONFLICT (event_exercise_id, event_team_id) DO UPDATE SET reason = EXCLUDED.reason,
                                                                declined_at = EXCLUDED.declined_at,
                                                                revision = EXCLUDED.revision,
                                                                mode = EXCLUDED.mode
    RETURNING event_exercise_id
)
UPDATE event_lab_reveal_barriers barrier
SET eligible_team_ids = array_remove(barrier.eligible_team_ids, sqlc.arg(team_id)::uuid)
WHERE barrier.event_id = sqlc.arg(event_id)
  AND barrier.event_exercise_id = sqlc.arg(event_exercise_id)
  AND barrier.opened_at IS NULL;
