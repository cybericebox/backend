-- Statistics collection for cyber-brain. Which statistics sources exist is code (today: events); the mode of a source is
-- set per source on the platform (the default, no row = off) and, for events, per event (no row = follow the default).
-- Modes: 1 off, 2 final (sent once the event ended), 3 continuous (sent while it runs). Only platform admins change them.
CREATE TABLE assistant_stats_defaults
(
    source     text        NOT NULL PRIMARY KEY,
    mode       smallint    NOT NULL CHECK (mode BETWEEN 1 AND 3),
    updated_at timestamptz NOT NULL
);

CREATE TABLE event_assistant_stats
(
    event_id   uuid        NOT NULL PRIMARY KEY REFERENCES events (id) ON DELETE CASCADE,
    mode       smallint    NOT NULL CHECK (mode BETWEEN 1 AND 3),
    updated_at timestamptz NOT NULL
);
