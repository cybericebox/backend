-- The keys the catalog editor seals its unsaved drafts with in the browser (flags and secret values included). One
-- key per person and browser: the browser names its key by the id in the cib_draft_key cookie. The key itself is
-- sealed with the platform key (PLATFORM_SECRETS_KEY), bound to the person and the id. A key unused for two days is
-- deleted (on read and by the periodic draft_key_gc job); signing out deletes the key of that browser.
CREATE TABLE user_draft_keys
(
    id             text        PRIMARY KEY,
    user_id        uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    key_ciphertext text        NOT NULL,
    created_at     timestamptz NOT NULL,
    last_used_at   timestamptz NOT NULL
);

CREATE INDEX user_draft_keys_user_idx ON user_draft_keys (user_id, last_used_at);
CREATE INDEX user_draft_keys_last_used_idx ON user_draft_keys (last_used_at);
