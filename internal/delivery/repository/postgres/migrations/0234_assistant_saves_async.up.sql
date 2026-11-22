-- A save whose task archive names device images imports in the background: the row is created when the import is queued
-- (job_id set, exercise_id still empty) and completed when the job is done. notified says the brain was told the
-- exercise id; a save that was imported but not yet announced is retried by the leader loop.
ALTER TABLE assistant_saves
    ALTER COLUMN exercise_id DROP NOT NULL,
    ADD COLUMN job_id   uuid,
    ADD COLUMN notified boolean NOT NULL DEFAULT false;
