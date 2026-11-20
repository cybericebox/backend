ALTER TABLE media_uploads
    DROP COLUMN IF EXISTS wrapped_key;

ALTER TABLE file_blobs
    DROP COLUMN IF EXISTS wrapped_key,
    DROP COLUMN IF EXISTS object_key;
