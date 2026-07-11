//! `pythia-eventlog` — the generic, append-only envelope store over the locked SQLite/WAL schema
//! (`docs/superpowers/data/pythia-data-model.md`).
//!
//! This crate knows nothing about the kernel's typed vocabulary (`UserCommand`, `LlmResponse`,
//! ...). It stores and reads the generic `EventRow` envelope; the kernel's translation layer
//! (`pythia-kernel`) serializes its typed events into that envelope, not the other way around.

mod schema;

use std::fmt;
use std::path::Path;

use rusqlite::{Connection, OptionalExtension};

/// Errors surfaced by the event log. Wraps the underlying SQLite error (e.g. a `CHECK` constraint
/// violation on insert, or the append-only triggers firing on `UPDATE`/`DELETE`) without hiding
/// it — callers that need to distinguish "rejected by a DB invariant" from "I/O failure" can match
/// on the inner `rusqlite::Error`.
#[derive(Debug, thiserror::Error)]
pub enum EventLogError {
    #[error("sqlite error: {0}")]
    Sqlite(#[from] rusqlite::Error),
    #[error("turn {0} is not open (already closed, or does not exist)")]
    TurnNotOpen(String),
}

pub type Result<T> = std::result::Result<T, EventLogError>;

/// A turn's identity. ULID, app-generated (time-sortable, no coordination needed) — see
/// data model doc §9. `seq` on `events`, not `turn_id`, is the load-bearing ordering key.
#[derive(Debug, Clone, PartialEq, Eq, Hash)]
pub struct TurnId(String);

impl TurnId {
    /// Mint a new, time-sortable turn identity.
    pub fn new() -> Self {
        TurnId(ulid::Ulid::new().to_string())
    }

    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl Default for TurnId {
    fn default() -> Self {
        Self::new()
    }
}

impl fmt::Display for TurnId {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl From<String> for TurnId {
    fn from(s: String) -> Self {
        TurnId(s)
    }
}

/// The two terminal outcomes a turn can be closed with. Deliberately excludes "open" — `Open` is
/// not a valid argument to `close_turn`, and modeling that as a narrower type (rather than
/// rejecting it at runtime from a three-variant `TurnStatus`) makes the invalid state
/// unrepresentable instead of a reachable panic in the public API.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TurnOutcome {
    Complete,
    Aborted,
}

impl TurnOutcome {
    fn as_db_str(self) -> &'static str {
        match self {
            TurnOutcome::Complete => "complete",
            TurnOutcome::Aborted => "aborted",
        }
    }

    /// The terminal event `type` that must accompany this outcome when closing a turn — derived,
    /// not caller-supplied, so the two can never disagree (data model §6: `turns.status` must
    /// never diverge from the presence of its terminal event).
    fn terminal_event_type(self) -> &'static str {
        match self {
            TurnOutcome::Complete => "TurnComplete",
            TurnOutcome::Aborted => "TurnAborted",
        }
    }
}

/// The generic envelope. The kernel's typed events serialize into this on write and deserialize
/// out of it on read; this crate has no opinion on what `event_type`/`payload_json` mean.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct EventRow {
    pub seq: i64,
    pub turn_id: TurnId,
    pub event_type: String,
    pub payload_json: String,
    pub effect_result: Option<String>,
    pub tainted: bool,
    pub created: String,
}

/// The fields needed to append an interior event. A small borrowed struct instead of positional
/// parameters — `seq` and `created` are DB-assigned, so this intentionally isn't the full
/// `EventRow`, but the named fields keep call sites self-describing (no bare `None, false`).
#[derive(Debug, Clone, Copy)]
pub struct NewEvent<'a> {
    pub event_type: &'a str,
    pub payload_json: &'a str,
    pub effect_result: Option<&'a str>,
    pub tainted: bool,
}

/// The append-only envelope store. One `EventLog` per SQLite file; the kernel holds the single
/// writer connection for the process (data model §6).
pub struct EventLog {
    conn: Connection,
}

impl EventLog {
    /// Open (or create) the store at `path`, applying the schema idempotently and setting the
    /// connection-level pragmas (`journal_mode=WAL`, `foreign_keys=ON`, `synchronous=FULL`).
    pub fn open(path: impl AsRef<Path>) -> Result<Self> {
        let conn = Connection::open(path)?;
        schema::apply(&conn)?;
        Ok(EventLog { conn })
    }

    /// Open an in-memory store — test convenience only; production callers use `open`.
    pub fn open_in_memory() -> Result<Self> {
        let conn = Connection::open_in_memory()?;
        schema::apply(&conn)?;
        Ok(EventLog { conn })
    }

    /// Open a new turn: insert the `turns` row and its opening `UserCommand` event in one atomic
    /// transaction (data model §6 — a turn must never exist with one but not the other).
    ///
    /// This does not itself enforce "at most one open turn" (the `idx_turns_open` partial index
    /// is intentionally non-unique — see the locked DDL). The caller is the single writer and
    /// owns that invariant, e.g. via `find_open_turn` before calling this.
    pub fn open_turn(&mut self, user_command_payload_json: &str, tainted: bool) -> Result<TurnId> {
        let turn_id = TurnId::new();
        let tx = self.conn.transaction()?;
        tx.execute(
            "INSERT INTO turns (turn_id, status) VALUES (?1, 'open')",
            (turn_id.as_str(),),
        )?;
        tx.execute(
            "INSERT INTO events (turn_id, type, payload_json, tainted) VALUES (?1, 'UserCommand', ?2, ?3)",
            (turn_id.as_str(), user_command_payload_json, tainted as i64),
        )?;
        tx.commit()?;
        Ok(turn_id)
    }

    /// Append a single event row. Single-row autocommit — every interior event is its own durable
    /// transaction (data model §6: the crash-resume guarantee depends on nothing batching two
    /// events into one commit). Returns the assigned monotonic `seq`.
    pub fn append(&self, turn_id: &TurnId, event: NewEvent<'_>) -> Result<i64> {
        self.conn.execute(
            "INSERT INTO events (turn_id, type, payload_json, effect_result, tainted) \
             VALUES (?1, ?2, ?3, ?4, ?5)",
            (
                turn_id.as_str(),
                event.event_type,
                event.payload_json,
                event.effect_result,
                event.tainted as i64,
            ),
        )?;
        Ok(self.conn.last_insert_rowid())
    }

    /// Read a turn's full event history, ordered by `seq` — served by `idx_events_turn_seq`, the
    /// query the kernel's context-compaction algorithm runs every LLM call.
    pub fn read_turn(&self, turn_id: &TurnId) -> Result<Vec<EventRow>> {
        let mut stmt = self.conn.prepare(
            "SELECT seq, turn_id, type, payload_json, effect_result, tainted, created \
             FROM events WHERE turn_id = ?1 ORDER BY seq",
        )?;
        let rows = stmt
            .query_map((turn_id.as_str(),), row_to_event)?
            .collect::<rusqlite::Result<Vec<_>>>()?;
        Ok(rows)
    }

    /// Find the (at most one, in this slice) currently-open turn — served by `idx_turns_open`.
    pub fn find_open_turn(&self) -> Result<Option<TurnId>> {
        let turn_id: Option<String> = self
            .conn
            .query_row(
                "SELECT turn_id FROM turns WHERE status = 'open' LIMIT 1",
                (),
                |row| row.get(0),
            )
            .optional()?;
        Ok(turn_id.map(TurnId::from))
    }

    /// Close a turn: update `turns.status`/`ended` and insert the terminal event, atomically
    /// (data model §6). The terminal event type is derived from `outcome` so the two can never
    /// disagree.
    ///
    /// The `UPDATE` is conditioned on `status = 'open'` and rolled back if it affects no row —
    /// closing an already-closed (or nonexistent) turn is rejected with `TurnNotOpen` rather than
    /// silently rewriting `status`/`ended` and appending a second, contradictory terminal event.
    pub fn close_turn(
        &mut self,
        turn_id: &TurnId,
        outcome: TurnOutcome,
        payload_json: &str,
    ) -> Result<()> {
        let tx = self.conn.transaction()?;
        let updated = tx.execute(
            "UPDATE turns SET status = ?1, ended = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') \
             WHERE turn_id = ?2 AND status = 'open'",
            (outcome.as_db_str(), turn_id.as_str()),
        )?;
        if updated != 1 {
            // Transaction drops (and rolls back) here without committing.
            return Err(EventLogError::TurnNotOpen(turn_id.as_str().to_string()));
        }
        tx.execute(
            "INSERT INTO events (turn_id, type, payload_json) VALUES (?1, ?2, ?3)",
            (
                turn_id.as_str(),
                outcome.terminal_event_type(),
                payload_json,
            ),
        )?;
        tx.commit()?;
        Ok(())
    }
}

fn row_to_event(row: &rusqlite::Row<'_>) -> rusqlite::Result<EventRow> {
    let turn_id: String = row.get(1)?;
    let tainted: i64 = row.get(5)?;
    Ok(EventRow {
        seq: row.get(0)?,
        turn_id: TurnId::from(turn_id),
        event_type: row.get(2)?,
        payload_json: row.get(3)?,
        effect_result: row.get(4)?,
        tainted: tainted != 0,
        created: row.get(6)?,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn open_log() -> EventLog {
        EventLog::open_in_memory().expect("open in-memory event log")
    }

    #[test]
    fn open_turn_inserts_turn_and_user_command_atomically_in_one_transaction() {
        let mut log = open_log();

        let turn_id = log.open_turn(r#"{"text":"hello"}"#, false).unwrap();

        let events = log.read_turn(&turn_id).unwrap();
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].event_type, "UserCommand");
        assert_eq!(events[0].payload_json, r#"{"text":"hello"}"#);
        assert_eq!(log.find_open_turn().unwrap(), Some(turn_id));
    }

    #[test]
    fn append_valid_tool_result_with_effect_result_returns_monotonic_seq() {
        let mut log = open_log();
        let turn_id = log.open_turn(r#"{"text":"hi"}"#, false).unwrap();

        let seq1 = log
            .append(
                &turn_id,
                NewEvent {
                    event_type: "LlmResponse",
                    payload_json: r#"{"text":"thinking"}"#,
                    effect_result: None,
                    tainted: false,
                },
            )
            .unwrap();
        let seq2 = log
            .append(
                &turn_id,
                NewEvent {
                    event_type: "ToolResult",
                    payload_json: r#"{"tool":"read_file"}"#,
                    effect_result: Some(r#"{"status":"ok"}"#),
                    tainted: false,
                },
            )
            .unwrap();

        assert!(seq2 > seq1);
    }

    #[test]
    fn append_tool_result_missing_effect_result_rejected_by_check_constraint() {
        let mut log = open_log();
        let turn_id = log.open_turn(r#"{"text":"hi"}"#, false).unwrap();

        let result = log.append(
            &turn_id,
            NewEvent {
                event_type: "ToolResult",
                payload_json: r#"{"tool":"read_file"}"#,
                effect_result: None,
                tainted: false,
            },
        );

        assert!(matches!(result, Err(EventLogError::Sqlite(_))));
    }

    #[test]
    fn append_non_tool_result_carrying_effect_result_rejected_by_check_constraint() {
        let mut log = open_log();
        let turn_id = log.open_turn(r#"{"text":"hi"}"#, false).unwrap();

        let result = log.append(
            &turn_id,
            NewEvent {
                event_type: "LlmResponse",
                payload_json: r#"{"text":"hi"}"#,
                effect_result: Some(r#"{"status":"ok"}"#),
                tainted: false,
            },
        );

        assert!(matches!(result, Err(EventLogError::Sqlite(_))));
    }

    #[test]
    fn update_existing_event_row_rejected_by_immutability_trigger() {
        let mut log = open_log();
        let turn_id = log.open_turn(r#"{"text":"hi"}"#, false).unwrap();

        let result = log.conn.execute(
            "UPDATE events SET payload_json = '{}' WHERE turn_id = ?1",
            (turn_id.as_str(),),
        );

        assert!(result.is_err());
    }

    #[test]
    fn delete_existing_event_row_rejected_by_immutability_trigger() {
        let mut log = open_log();
        let turn_id = log.open_turn(r#"{"text":"hi"}"#, false).unwrap();

        let result = log
            .conn
            .execute("DELETE FROM events WHERE turn_id = ?1", (turn_id.as_str(),));

        assert!(result.is_err());
    }

    #[test]
    fn read_turn_returns_rows_ordered_by_seq_matches_idx_events_turn_seq() {
        let mut log = open_log();
        let turn_id = log.open_turn(r#"{"text":"hi"}"#, false).unwrap();
        log.append(
            &turn_id,
            NewEvent {
                event_type: "LlmResponse",
                payload_json: r#"{"text":"a"}"#,
                effect_result: None,
                tainted: false,
            },
        )
        .unwrap();
        log.append(
            &turn_id,
            NewEvent {
                event_type: "ToolResult",
                payload_json: r#"{"tool":"read_file"}"#,
                effect_result: Some(r#"{"status":"ok"}"#),
                tainted: false,
            },
        )
        .unwrap();

        let events = log.read_turn(&turn_id).unwrap();

        let seqs: Vec<i64> = events.iter().map(|e| e.seq).collect();
        let mut sorted = seqs.clone();
        sorted.sort();
        assert_eq!(seqs, sorted);
        assert_eq!(events.len(), 3);
    }

    #[test]
    fn find_open_turn_zero_open_turns_returns_none() {
        let log = open_log();

        assert_eq!(log.find_open_turn().unwrap(), None);
    }

    #[test]
    fn find_open_turn_one_open_turn_returns_it() {
        let mut log = open_log();
        let turn_id = log.open_turn(r#"{"text":"hi"}"#, false).unwrap();

        assert_eq!(log.find_open_turn().unwrap(), Some(turn_id));
    }

    #[test]
    fn close_turn_updates_status_and_ended_atomically_with_terminal_event() {
        let mut log = open_log();
        let turn_id = log.open_turn(r#"{"text":"hi"}"#, false).unwrap();

        log.close_turn(&turn_id, TurnOutcome::Complete, "{}")
            .unwrap();

        assert_eq!(log.find_open_turn().unwrap(), None);
        let events = log.read_turn(&turn_id).unwrap();
        assert_eq!(events.last().unwrap().event_type, "TurnComplete");

        let status: String = log
            .conn
            .query_row(
                "SELECT status FROM turns WHERE turn_id = ?1",
                (turn_id.as_str(),),
                |row| row.get(0),
            )
            .unwrap();
        assert_eq!(status, "complete");
        let ended: Option<String> = log
            .conn
            .query_row(
                "SELECT ended FROM turns WHERE turn_id = ?1",
                (turn_id.as_str(),),
                |row| row.get(0),
            )
            .unwrap();
        assert!(ended.is_some());
    }

    #[test]
    fn close_turn_aborted_inserts_turn_aborted_event() {
        let mut log = open_log();
        let turn_id = log.open_turn(r#"{"text":"hi"}"#, false).unwrap();

        log.close_turn(&turn_id, TurnOutcome::Aborted, r#"{"reason":"crash"}"#)
            .unwrap();

        let events = log.read_turn(&turn_id).unwrap();
        assert_eq!(events.last().unwrap().event_type, "TurnAborted");
    }

    #[test]
    fn close_turn_already_closed_turn_rejected_without_second_terminal_event() {
        let mut log = open_log();
        let turn_id = log.open_turn(r#"{"text":"hi"}"#, false).unwrap();
        log.close_turn(&turn_id, TurnOutcome::Complete, "{}")
            .unwrap();

        let result = log.close_turn(&turn_id, TurnOutcome::Aborted, r#"{"reason":"double"}"#);

        assert!(matches!(result, Err(EventLogError::TurnNotOpen(id)) if id == turn_id.as_str()));

        let status: String = log
            .conn
            .query_row(
                "SELECT status FROM turns WHERE turn_id = ?1",
                (turn_id.as_str(),),
                |row| row.get(0),
            )
            .unwrap();
        assert_eq!(status, "complete");

        let events = log.read_turn(&turn_id).unwrap();
        let terminal_count = events
            .iter()
            .filter(|e| e.event_type == "TurnComplete" || e.event_type == "TurnAborted")
            .count();
        assert_eq!(terminal_count, 1);
    }

    #[test]
    fn reopen_same_file_path_schema_apply_is_idempotent() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("eventlog.sqlite3");

        let mut log1 = EventLog::open(&path).unwrap();
        let turn_id = log1.open_turn(r#"{"text":"hi"}"#, false).unwrap();
        log1.close_turn(&turn_id, TurnOutcome::Complete, "{}")
            .unwrap();
        drop(log1);

        // Reopening against the same file must not error even though the schema already exists.
        let log2 = EventLog::open(&path).unwrap();
        let events = log2.read_turn(&turn_id).unwrap();
        assert_eq!(events.len(), 2);
    }

    #[test]
    fn tainted_flag_round_trips_through_append_and_read() {
        let mut log = open_log();
        let turn_id = log.open_turn(r#"{"text":"hi"}"#, true).unwrap();

        let events = log.read_turn(&turn_id).unwrap();

        assert!(events[0].tainted);
    }
}
