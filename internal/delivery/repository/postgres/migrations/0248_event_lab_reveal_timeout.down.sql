DROP FUNCTION IF EXISTS event_team_lab_reveal_ready(uuid, uuid);
ALTER TABLE event_lab_reveal_barriers
    DROP COLUMN IF EXISTS timeout_alarm_sent_at,
    DROP COLUMN IF EXISTS timed_out_at;
