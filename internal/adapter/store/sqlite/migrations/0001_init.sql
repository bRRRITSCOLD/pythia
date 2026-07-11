-- 0001_init.sql
-- Pythia first slice: two aggregates — Session (root) and its ordered Message history.
-- Single messages table with typed columns + JSON tool_calls (see docs/data/first-slice-schema.md §3).

CREATE TABLE sessions (
    id         TEXT PRIMARY KEY NOT NULL,
    title      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE messages (
    id           TEXT PRIMARY KEY NOT NULL,
    session_id   TEXT NOT NULL,
    seq          INTEGER NOT NULL,
    role         TEXT NOT NULL
                 CHECK (role IN ('system','user','assistant','tool')),
    content      TEXT NOT NULL DEFAULT '',
    tool_calls   TEXT
                 CHECK (tool_calls IS NULL OR json_valid(tool_calls)),
    tool_call_id TEXT,
    created_at   TEXT NOT NULL,
    FOREIGN KEY (session_id) REFERENCES sessions(id),
    UNIQUE (session_id, seq)
) STRICT;
