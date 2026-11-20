-- name: CreateFile :one
-- One statement: bump (or create) the blob's refcount and insert the logical
-- file row pointing at it. Also (re)stamps the blob's touched_at to this
-- call's `now` — both on first insert and on a dedup-hit conflict — so GC's
-- orphan listing (ListOrphanBlobs) never targets a blob a concurrent upload
-- just referenced (see file_blobs.touched_at, migration 0015).
-- object_key and wrapped_key are those of the blob when it is created; on a
-- dedup hit the blob keeps its own, and the row returned says which they are,
-- so the caller can drop the object it stored if it lost the race.
-- require_existing: the caller stored no object because it found the blob. Then the
-- blob is only bumped, never created: if the collector removed it meanwhile, nothing is
-- inserted and no row comes back (the caller starts again), so a blob can never come
-- back as an empty row that points at an object that is gone.
WITH bumped AS (
    UPDATE file_blobs
        SET ref_count = ref_count + 1, touched_at = sqlc.arg(created_at)
        WHERE file_blobs.content_hash = sqlc.arg(content_hash) AND sqlc.arg(require_existing)::boolean
        RETURNING object_key, wrapped_key),
     inserted AS (
         INSERT INTO file_blobs (content_hash, size_bytes, ref_count, created_at, touched_at, object_key, wrapped_key)
             SELECT sqlc.arg(content_hash), sqlc.arg(size_bytes), 1, sqlc.arg(created_at), sqlc.arg(created_at),
                    sqlc.arg(object_key), sqlc.arg(wrapped_key)
             WHERE NOT sqlc.arg(require_existing)::boolean
             ON CONFLICT (content_hash) DO UPDATE SET ref_count = file_blobs.ref_count + 1, touched_at = excluded.touched_at
             RETURNING object_key, wrapped_key),
     blob AS (SELECT object_key, wrapped_key FROM bumped UNION ALL SELECT object_key, wrapped_key FROM inserted),
     created AS (
         INSERT INTO files (id, name, content_type, size_bytes, content_hash, created_at, created_by)
             SELECT sqlc.arg(id), sqlc.arg(name), sqlc.arg(content_type), sqlc.arg(size_bytes), sqlc.arg(content_hash),
                    sqlc.arg(created_at), sqlc.arg(created_by)
             WHERE EXISTS (SELECT 1 FROM blob)
             RETURNING id, name, content_type, size_bytes, content_hash, created_at, created_by)
SELECT created.id,
       created.name,
       created.content_type,
       created.size_bytes,
       created.content_hash,
       created.created_at,
       created.created_by,
       blob.object_key  AS blob_object_key,
       blob.wrapped_key AS blob_wrapped_key
FROM created,
     blob;

-- name: GetFileBlob :one
SELECT content_hash, size_bytes, object_key, wrapped_key
FROM file_blobs
WHERE content_hash = $1;

-- name: AdoptBlobEncryption :execrows
-- Moves a plain blob to an encrypted object: only while the blob is still plain, so of two writers one wins.
UPDATE file_blobs
SET object_key  = sqlc.arg(object_key),
    wrapped_key = sqlc.arg(wrapped_key)
WHERE content_hash = sqlc.arg(content_hash)
  AND wrapped_key = '';

-- name: ListPlainBlobs :many
-- The blobs still stored plain that the encryption pass has to move, after the cursor: every plain blob except one that
-- only public images use (some file of it is referenced by a public kind and none by any other).
SELECT b.content_hash, b.size_bytes, b.object_key
FROM file_blobs b
WHERE b.wrapped_key = ''
  AND b.content_hash > sqlc.arg(after)
  AND NOT (EXISTS (SELECT 1
                   FROM files f
                            JOIN file_references r ON r.file_id = f.id
                   WHERE f.content_hash = b.content_hash
                     AND r.ref_type = ANY (sqlc.arg(public_ref_types)::text[]))
    AND NOT EXISTS (SELECT 1
                    FROM files f
                             JOIN file_references r ON r.file_id = f.id
                    WHERE f.content_hash = b.content_hash
                      AND NOT (r.ref_type = ANY (sqlc.arg(public_ref_types)::text[]))))
ORDER BY b.content_hash
LIMIT sqlc.arg(row_limit);

-- name: CountPlainBlobs :one
SELECT count(*)::bigint
FROM file_blobs
WHERE wrapped_key = '';

-- name: GetFileByID :one
SELECT *
FROM files
WHERE id = $1;

-- name: BlobExists :one
SELECT EXISTS(SELECT 1 FROM file_blobs WHERE content_hash = $1)::boolean;

-- name: ReplaceFileReferences :exec
-- Diff-style sync: drop references not in the new set, insert missing ones.
-- Kept rows are never touched, so the delete CTE cannot collide with the
-- insert (same-statement visibility).
WITH del AS (
    DELETE FROM file_references
        WHERE ref_type = sqlc.arg(ref_type) AND ref_id = sqlc.arg(ref_id)
            AND NOT (file_id = ANY (sqlc.arg(file_ids)::uuid[])))
INSERT
INTO file_references (ref_type, ref_id, file_id)
SELECT sqlc.arg(ref_type), sqlc.arg(ref_id), unnest(sqlc.arg(file_ids)::uuid[])
ON CONFLICT DO NOTHING;

-- name: GetFileReferenceIDs :many
-- Read path for an owner's current file set (e.g. resolving a user's avatar
-- file before streaming it). Ordered newest-file-first (files.created_at
-- DESC, file_id DESC as a deterministic tiebreak): a concurrent double-upload
-- can leave more than one reference row for the same owner, and a caller that
-- wants "the current" one (e.g. GetAvatar picking index 0) needs a
-- deterministic, not incidental, answer. Repositories don't validate on read:
-- a ref_type/ref_id with no rows simply yields an empty slice, not an error.
SELECT r.file_id
FROM file_references r
         JOIN files f ON f.id = r.file_id
WHERE r.ref_type = $1
  AND r.ref_id = $2
ORDER BY f.created_at DESC, r.file_id DESC;

-- name: AddFileReference :exec
-- An email image is owned by its draft as soon as it is uploaded. Inserting
-- one link avoids a read/replace race when uploads finish concurrently.
INSERT INTO file_references (ref_type, ref_id, file_id)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;

-- name: DeleteFileReferences :execrows
DELETE
FROM file_references
WHERE ref_type = $1
  AND ref_id = $2;

-- name: DeleteFileReferencesBatch :execrows
DELETE
FROM file_references
WHERE ref_type = $1
  AND ref_id = ANY (sqlc.arg(ref_ids)::uuid[]);

-- name: DeleteUnreferencedFiles :many
-- GC step 1: drop grace-expired unreferenced files, decrement their blobs.
WITH doomed AS (
    DELETE FROM files f
        WHERE f.created_at < sqlc.arg(created_before)
            AND NOT EXISTS (SELECT 1 FROM file_references r WHERE r.file_id = f.id)
        RETURNING f.content_hash),
     counted AS (SELECT content_hash, count(*) AS cnt FROM doomed GROUP BY content_hash)
UPDATE file_blobs b
SET ref_count = b.ref_count - counted.cnt
FROM counted
WHERE b.content_hash = counted.content_hash
RETURNING b.content_hash, b.ref_count;

-- name: ListOrphanBlobs :many
-- touched_at < touched_before (the same GC grace cutoff DeleteUnreferencedFiles
-- uses) gives a concurrent dedup upload of this exact content a grace window
-- to complete its CreateFile bump before GC ever considers the blob a
-- deletion candidate — see file_blobs.touched_at, migration 0015.
SELECT content_hash, object_key, (wrapped_key <> '')::boolean AS encrypted
FROM file_blobs
WHERE ref_count <= 0
  AND touched_at < sqlc.arg(touched_before);

-- name: DeleteBlob :execrows
-- Guarded delete: refuses when the blob got re-referenced between the S3
-- removal decision and this call.
DELETE
FROM file_blobs
WHERE content_hash = $1
  AND ref_count <= 0
  AND wrapped_key = '';

-- name: DeleteEncryptedBlob :one
-- The row goes first and says which object it held at that moment (the pass that encrypts older blobs may have moved it
-- since the listing); the caller removes that object. Guarded as DeleteBlob, and only for a blob that is encrypted now.
DELETE
FROM file_blobs
WHERE content_hash = sqlc.arg(content_hash)
  AND ref_count <= 0
  AND touched_at < sqlc.arg(touched_before)
  AND wrapped_key <> ''
RETURNING object_key;

-- name: AddObjectRemoval :exec
-- An object that could not be removed: kept here until a pass removes it.
INSERT INTO object_removals (object_key, created_at)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: ListObjectRemovals :many
SELECT object_key
FROM object_removals
WHERE created_at < $1
ORDER BY created_at
LIMIT $2;

-- name: DeleteObjectRemoval :exec
DELETE
FROM object_removals
WHERE object_key = $1;

-- name: ListUserIDsAfter :many
-- Every user, deleted ones too, after the cursor: the pass that moves legacy avatars probes avatars/<id> for each.
SELECT id, (deleted_at IS NOT NULL)::boolean AS deleted
FROM users
WHERE id > sqlc.arg(after)
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- name: AdoptLegacyAvatar :execrows
-- Gives a user the avatar reference, in this one statement, only while the legacy avatar is still the user's current one:
-- the user exists (not deleted), still has a picture (RemoveAvatar clears it first, in a write that commits before it touches
-- anything else) and has no avatar reference. The user row is locked FOR UPDATE, so a removal or a deletion cannot slip between
-- the check and the insert. Zero rows: the avatar was removed, the user is gone or has an avatar of their own; the file stays
-- unreferenced and the media GC takes it.
INSERT INTO file_references (ref_type, ref_id, file_id)
SELECT 'user_avatar', u.id, sqlc.arg(file_id)::uuid
FROM (SELECT id FROM users WHERE id = sqlc.arg(user_id) AND deleted_at IS NULL AND picture <> '' FOR UPDATE) u
WHERE NOT EXISTS (SELECT 1 FROM file_references r WHERE r.ref_type = 'user_avatar' AND r.ref_id = u.id);
