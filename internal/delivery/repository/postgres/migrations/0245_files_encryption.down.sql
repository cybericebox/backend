-- Refuses while anything is encrypted: dropping the columns would make every encrypted object unreadable for good, and an
-- older binary could not read them either. Decrypt nothing, restore nothing: take the database from before the upgrade.
DO
$$
    BEGIN
        IF EXISTS (SELECT 1 FROM file_blobs WHERE wrapped_key <> '')
            OR EXISTS (SELECT 1 FROM media_uploads WHERE wrapped_key <> '') THEN
            RAISE EXCEPTION 'migration 0245 cannot be reverted: encrypted files exist and would become unreadable';
        END IF;
    END
$$;

DROP TABLE IF EXISTS object_removals;

ALTER TABLE media_uploads
    DROP COLUMN IF EXISTS wrapped_key;

ALTER TABLE file_blobs
    DROP COLUMN IF EXISTS wrapped_key,
    DROP COLUMN IF EXISTS object_key;
