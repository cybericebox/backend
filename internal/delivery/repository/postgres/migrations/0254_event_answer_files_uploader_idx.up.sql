-- The upload caps of participant answer files read one person's files (GetEventAnswerFileUsage): per event, and
-- their unattached files across every event.
CREATE INDEX IF NOT EXISTS event_answer_files_uploader_idx
    ON event_answer_files (uploaded_by, event_id) INCLUDE (size_bytes, attached_at, scope, field_key);
