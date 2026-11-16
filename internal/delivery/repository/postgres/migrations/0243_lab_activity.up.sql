-- What the laboratory's activity reports (contract 2.5) say about the labs, kept by the daemon only as long as the statistics for
-- cyber-brain need it. The laboratory hands out raw facts; the daemon folds each page into these small running figures in the
-- same statement that moves the cursor, so a page is counted exactly once.
--
--   lab_activity_cursors   where the next pull of a group's activity starts (the cursor the laboratory gave, kept unchanged);
--                          gap says the laboratory dropped events the cursor had not reached, so the figures are a lower bound.
--   lab_activity           one row per lab seen in the reports: egress_used = the lab had any Internet egress. Egress is a fact
--                          of the lab, never of a device or a user.
--   lab_activity_clients   bytes (out + in) a VPN client exchanged with the lab's addresses.
--   lab_activity_dsts      the distinct lab addresses a VPN client reached.
--   lab_activity_sessions  console sessions: opened_at, and closed_at once the session ended.
--   assistant_runs.lab_activity  the activity figures of a grader run's lab, read just before the lab is destroyed (the lab and
--                          its test deploy are gone when the run's lab facts are sent): null until decided, then the figures or
--                          {"reported":false}.
ALTER TABLE assistant_runs
    ADD COLUMN lab_activity jsonb NULL;

CREATE TABLE lab_activity_cursors
(
    lab_group_name text        PRIMARY KEY,
    cursor         text        NOT NULL,
    gap            boolean     NOT NULL DEFAULT false,
    updated_at     timestamptz NOT NULL
);

CREATE TABLE lab_activity
(
    lab_group_name text        NOT NULL,
    lab_name       text        NOT NULL,
    egress_used    boolean     NOT NULL DEFAULT false,
    updated_at     timestamptz NOT NULL,
    PRIMARY KEY (lab_group_name, lab_name)
);
CREATE INDEX lab_activity_updated_idx ON lab_activity (updated_at);

CREATE TABLE lab_activity_clients
(
    lab_group_name text   NOT NULL,
    lab_name       text   NOT NULL,
    client         text   NOT NULL,
    bytes          bigint NOT NULL DEFAULT 0 CHECK (bytes >= 0),
    PRIMARY KEY (lab_group_name, lab_name, client)
);

CREATE TABLE lab_activity_dsts
(
    lab_group_name text NOT NULL,
    lab_name       text NOT NULL,
    client         text NOT NULL,
    dst_address    text NOT NULL,
    PRIMARY KEY (lab_group_name, lab_name, client, dst_address)
);

CREATE TABLE lab_activity_sessions
(
    lab_group_name text        NOT NULL,
    lab_name       text        NOT NULL,
    session_id     text        NOT NULL,
    client         text        NOT NULL,
    opened_at      timestamptz NOT NULL,
    closed_at      timestamptz NULL,
    PRIMARY KEY (lab_group_name, lab_name, session_id)
);
