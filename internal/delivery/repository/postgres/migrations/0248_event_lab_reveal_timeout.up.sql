-- The reveal timeout of an all_ready barrier: timed_out_at is when the timeout opened it (NULL: it opened because every
-- team was Ready, or it is still closed), timeout_alarm_sent_at when the organizers' alarm about the stuck teams went
-- out. Both are set in the same statement that opens the barrier and cleared with opened_at when the tuple changes, so
-- an alarm that fails stays due and is retried by the next stand pass.
ALTER TABLE event_lab_reveal_barriers
    ADD COLUMN timed_out_at          timestamptz,
    ADD COLUMN timeout_alarm_sent_at timestamptz;

-- The one Ready test of a team's lab for a task set (event exercise), used by the barrier itself, by the publish of
-- the tasks behind an opened barrier and by the stuck list of the timeout alarm: the lab is materialized, of the set's
-- current definition, running and observed at its desired revision, has a binding at its current generation, and
-- every binding is ready.
CREATE OR REPLACE FUNCTION event_team_lab_reveal_ready(p_team_id uuid, p_event_exercise_id uuid)
    RETURNS boolean
    LANGUAGE sql
    STABLE
AS
$$
SELECT EXISTS(SELECT 1 FROM event_team_labs l WHERE l.event_team_id=p_team_id AND l.event_exercise_id=p_event_exercise_id
  AND l.materialized AND l.agent_uid<>'' AND l.agent_generation>0 AND l.desired_state='Running' AND l.logical_closed_at IS NULL
  AND l.actual_state='Running' AND l.runtime_ready
  AND l.definition_version_id=(SELECT ee.exercise_version_id FROM event_exercises ee WHERE ee.id=p_event_exercise_id)
  AND EXISTS(SELECT 1 FROM lab_bindings b WHERE b.lab_id=l.id AND b.generation=l.generation AND b.lab_group_name=l.lab_group_name AND b.lab_name=l.lab_name)
  AND l.observed_revision=l.desired_revision
  AND NOT EXISTS(SELECT 1 FROM lab_bindings b WHERE b.lab_id=l.id AND b.readiness<>1))
$$;
