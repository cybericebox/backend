-- Encryption of the objects in the files bucket.
--
--   file_blobs.object_key   where the object of the blob lies in the bucket. Empty: the older place media/blobs/<content_hash>
--                           (a plain object). A name here is random (media/enc/<id>) and says nothing about the content.
--   file_blobs.wrapped_key  the random key of that object, wrapped with the key of the `files` purpose (v1:<key id>:...). Empty:
--                           the object is plain (a legacy object, or a public image, which is stored plain on purpose). The key id
--                           of the master key that wrapped it is inside the value, as in every sealed value of the platform.
--   media_uploads.wrapped_key  the same for the chunk objects of an upload in progress (one key, one stream per chunk).
--
-- Deleting the blob row destroys the wrapped key, so a copy of the object that is left in a versioned bucket cannot be read.
ALTER TABLE file_blobs
    ADD COLUMN object_key  text NOT NULL DEFAULT '',
    ADD COLUMN wrapped_key text NOT NULL DEFAULT '';

ALTER TABLE media_uploads
    ADD COLUMN wrapped_key text NOT NULL DEFAULT '';
