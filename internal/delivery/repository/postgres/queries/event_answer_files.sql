-- name: CreateEventAnswerFile :exec
-- One statement for the row and the reference that keeps the blob alive, so
-- retention never sees one without the other.
WITH answer_file AS (
    INSERT INTO event_answer_files (file_id, event_id, scope, field_key, name, size_bytes, content_type, uploaded_by, created_at)
    VALUES (sqlc.arg(file_id), sqlc.arg(event_id), sqlc.arg(scope), sqlc.arg(field_key), sqlc.arg(name),
            sqlc.arg(size_bytes), sqlc.arg(content_type), sqlc.arg(uploaded_by), sqlc.arg(created_at))
    RETURNING file_id)
INSERT INTO file_references (ref_type, ref_id, file_id)
SELECT 'event_answer_file', file_id, file_id FROM answer_file;

-- name: GetEventAnswerFile :one
SELECT * FROM event_answer_files WHERE file_id = $1;

-- name: ListEventAnswerFiles :many
SELECT * FROM event_answer_files WHERE file_id = ANY(sqlc.arg(file_ids)::uuid[]);

-- name: AttachEventAnswerFiles :execrows
-- Gives the answer's files to their owner and releases the files the owner's
-- previous answers held but the saved answer no longer references.
WITH released AS (
    DELETE FROM event_answer_files f
    WHERE f.event_id = sqlc.arg(event_id)
      AND f.scope = sqlc.arg(scope)
      AND f.owner_id = sqlc.arg(owner_id)
      AND NOT (f.file_id = ANY(sqlc.arg(file_ids)::uuid[]))
    RETURNING f.file_id),
     unreferenced AS (
         DELETE FROM file_references r
         WHERE r.ref_type = 'event_answer_file'
           AND r.ref_id IN (SELECT file_id FROM released))
UPDATE event_answer_files f
SET owner_id    = sqlc.arg(owner_id),
    attached_at = COALESCE(f.attached_at, sqlc.arg(attached_at)::timestamptz)
WHERE f.event_id = sqlc.arg(event_id)
  AND f.scope = sqlc.arg(scope)
  AND f.file_id = ANY(sqlc.arg(file_ids)::uuid[]);

-- name: GetEventAnswerFileUsage :one
-- What one person has uploaded to an event: the uploads of one question still
-- waiting for an answer, and the bytes of every file they uploaded there
-- (attached or not), for the upload caps.
SELECT count(*) FILTER (WHERE attached_at IS NULL AND scope = sqlc.arg(scope) AND field_key = sqlc.arg(field_key))::bigint AS pending_in_field,
       COALESCE(sum(size_bytes), 0)::bigint                                                                              AS total_bytes
FROM event_answer_files
WHERE event_id = sqlc.arg(event_id)
  AND uploaded_by = sqlc.arg(uploaded_by);
