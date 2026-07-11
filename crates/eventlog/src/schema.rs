//! DDL for the event log store, frozen per `docs/superpowers/data/pythia-data-model.md`.
//!
//! Applied idempotently (`CREATE TABLE/INDEX/TRIGGER IF NOT EXISTS`) so repeated `EventLog::open`
//! calls against the same file never error.

pub(crate) const DDL: &str = r#"
CREATE TABLE IF NOT EXISTS turns (
    turn_id     TEXT PRIMARY KEY,
    status      TEXT NOT NULL DEFAULT 'open'
                    CHECK (status IN ('open', 'complete', 'aborted')),
    created     TEXT NOT NULL
                    DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    ended       TEXT NULL
);

CREATE INDEX IF NOT EXISTS idx_turns_open ON turns(status) WHERE status = 'open';

CREATE TABLE IF NOT EXISTS events (
    seq             INTEGER PRIMARY KEY AUTOINCREMENT,
    turn_id         TEXT NOT NULL REFERENCES turns(turn_id),
    type            TEXT NOT NULL
                        CHECK (type IN (
                            'UserCommand',
                            'LlmResponse',
                            'ToolResult',
                            'TurnComplete',
                            'TurnAborted'
                        )),
    payload_json    TEXT NOT NULL
                        CHECK (json_valid(payload_json)),
    effect_result   TEXT NULL
                        CHECK (effect_result IS NULL OR json_valid(effect_result)),
    tainted         INTEGER NOT NULL DEFAULT 0
                        CHECK (tainted IN (0, 1)),
    created         TEXT NOT NULL
                        DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),

    CHECK (
        (type = 'ToolResult' AND effect_result IS NOT NULL)
        OR (type != 'ToolResult' AND effect_result IS NULL)
    )
);

CREATE TRIGGER IF NOT EXISTS trg_events_no_update
BEFORE UPDATE ON events
BEGIN
    SELECT RAISE(ABORT, 'events are append-only: UPDATE forbidden');
END;

CREATE TRIGGER IF NOT EXISTS trg_events_no_delete
BEFORE DELETE ON events
BEGIN
    SELECT RAISE(ABORT, 'events are append-only: DELETE forbidden');
END;

CREATE INDEX IF NOT EXISTS idx_events_turn_seq ON events(turn_id, seq);

CREATE INDEX IF NOT EXISTS idx_events_tool_result ON events(turn_id, seq) WHERE type = 'ToolResult';

CREATE INDEX IF NOT EXISTS idx_events_tainted ON events(turn_id, seq) WHERE tainted = 1;
"#;

/// Apply the schema and set the connection-level pragmas the data model requires. Idempotent —
/// safe to call every time a connection is opened against the same file.
pub(crate) fn apply(conn: &rusqlite::Connection) -> rusqlite::Result<()> {
    // journal_mode returns the resulting mode as a row, so it needs pragma_update_and_check,
    // not pragma_update (which errors if the pragma returns data).
    conn.pragma_update_and_check(None, "journal_mode", "WAL", |_row| Ok(()))?;
    conn.pragma_update(None, "foreign_keys", "ON")?;
    conn.pragma_update(None, "synchronous", "FULL")?;
    conn.execute_batch(DDL)?;
    Ok(())
}
