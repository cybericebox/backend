-- The stable error code (our catalog's FullCode) a failed save ended with: it is what the brain is told by save_failed. A
-- failed save whose notified flag is still false is announced to the brain by the leader loop, like a done one.
ALTER TABLE assistant_saves
    ADD COLUMN failure_code integer NOT NULL DEFAULT 0;
