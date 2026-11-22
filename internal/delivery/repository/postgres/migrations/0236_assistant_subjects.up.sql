-- The opaque pseudonym a user has at cyber-brain: random, per user, created on first use. The brain binds its chat
-- sessions to it and never sees the platform id. The row is deleted with the account's personal data (the purge of
-- deleted accounts), which cuts the link; what the brain kept stays unlinked.
CREATE TABLE assistant_subjects
(
    user_id    uuid        NOT NULL PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    subject    uuid        NOT NULL UNIQUE,
    created_at timestamptz NOT NULL
);
