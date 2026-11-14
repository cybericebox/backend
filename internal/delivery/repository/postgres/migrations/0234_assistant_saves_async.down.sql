DELETE FROM assistant_saves WHERE exercise_id IS NULL;
ALTER TABLE assistant_saves
    DROP COLUMN notified,
    DROP COLUMN job_id,
    ALTER COLUMN exercise_id SET NOT NULL;
