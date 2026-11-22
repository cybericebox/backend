-- The task a finished assistant session was saved as, one row per (brain session, author): a repeated save of the same
-- session by the same author does not import the task again. The primary key is the guard against two saves at once.
CREATE TABLE assistant_saves
(
    session_id  text        NOT NULL,
    author_id   uuid        NOT NULL,
    exercise_id uuid        NOT NULL,
    created_at  timestamptz NOT NULL,
    PRIMARY KEY (session_id, author_id)
);
