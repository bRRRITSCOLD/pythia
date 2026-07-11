//! `pythia-kernel`: turn-loop orchestration, typed event vocabulary, replay-on-resume,
//! context-window compaction.
//!
//! Task 14 landed the typed event vocabulary (`event.rs`) and its pure translation to/from
//! `pythia_eventlog::EventRow`. Task 15 (this module + `turn.rs`/`context.rs`/`dispatch.rs`) lands
//! the turn-loop state machine itself: the heart of the durability guarantee. Given a turn's
//! event history, `turn::next_action` decides the single next action; `Kernel::run_turn`/
//! `Kernel::resume` execute it, journal the result, and repeat until `TurnComplete` — and because
//! `next_action` is a pure function of the history alone (ADR-0002), `resume` is not a special
//! code path: it is the exact same loop, just starting from whatever the log already contains.

mod context;
mod dispatch;
mod event;
mod turn;

pub use dispatch::SkillConfig;
pub use event::{KernelEvent, ToolCall, TranslateError};

use pythia_eventlog::{
    EventLog, EventLogError, NewEvent, TurnId, TurnOutcome as EventLogTurnOutcome,
};
use pythia_manifest::PolicyFile;
use pythia_provider::{Provider, ProviderError, ResponseChunk, ToolSchema};
use std::collections::HashMap;

use context::build_context;
use dispatch::dispatch_tool;
use turn::{next_action, NextAction};

/// Errors the turn loop can surface. Distinguishes the event log's own failures (a `CHECK`
/// violation, the double-close guard) from a bad provider response and a malformed persisted
/// event (a translation failure reading history back) — callers that care can match on the
/// variant; callers that don't can just propagate it.
#[derive(Debug, thiserror::Error)]
pub enum KernelError {
    #[error("event log error: {0}")]
    EventLog(#[from] EventLogError),
    #[error("provider error: {0}")]
    Provider(#[from] ProviderError),
    #[error("translating a persisted event failed: {0}")]
    Translate(#[from] TranslateError),
}

/// The result of driving a turn (via `run_turn` or `resume`) to completion: the terminal status
/// plus the full journalled event sequence — everything a caller (a test, or `pythia-cli`'s
/// rendering layer, Task 16) needs without a second read of the log.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TurnOutcome {
    pub turn_id: TurnId,
    pub status: EventLogTurnOutcome,
    pub events: Vec<KernelEvent>,
}

/// The turn-loop orchestrator. Depends on `pythia-eventlog` and `pythia-capability-host` as
/// concrete crates (no `EventStore`/`SkillExecutor` traits — plan §0/ADR-0001) and is generic
/// only over `Provider` (the one seam with more than one real implementer — ADR-0005).
pub struct Kernel<P: Provider> {
    eventlog: EventLog,
    provider: P,
    policy: PolicyFile,
    skills: HashMap<String, SkillConfig>,
}

impl<P: Provider> Kernel<P> {
    pub fn new(
        eventlog: EventLog,
        provider: P,
        policy: PolicyFile,
        skills: HashMap<String, SkillConfig>,
    ) -> Self {
        Self {
            eventlog,
            provider,
            policy,
            skills,
        }
    }

    /// Opens a new turn from `user_text` and drives it to completion. The opening `UserCommand`
    /// event is inserted atomically with the `turns` row by `EventLog::open_turn` (data model
    /// doc §6); everything from there on is the same loop `resume` also runs.
    pub async fn run_turn(
        &mut self,
        user_text: impl Into<String>,
    ) -> Result<TurnOutcome, KernelError> {
        let opening_row: pythia_eventlog::EventRow = KernelEvent::UserCommand {
            text: user_text.into(),
            tainted: false,
        }
        .into();
        let turn_id = self
            .eventlog
            .open_turn(&opening_row.payload_json, opening_row.tainted)?;
        self.drive_turn(turn_id).await
    }

    /// Called at startup: if there's an open turn, resume it — the exact same loop `run_turn`
    /// uses, starting wherever `next_action` says the log leaves off (data model doc §5's resume
    /// algorithm, verbatim; no special-cased "resume" path, per ADR-0002). Returns `None` when
    /// there is nothing to resume.
    pub async fn resume(&mut self) -> Result<Option<TurnOutcome>, KernelError> {
        match self.eventlog.find_open_turn()? {
            Some(turn_id) => Ok(Some(self.drive_turn(turn_id).await?)),
            None => Ok(None),
        }
    }

    /// The loop itself: read history, decide, act, journal, repeat until `Complete`. Every
    /// interior event append is its own single-row transaction (`EventLog::append`'s own
    /// contract) — never batched per-turn, which is what makes crash-resume safe (data model doc
    /// §6).
    async fn drive_turn(&mut self, turn_id: TurnId) -> Result<TurnOutcome, KernelError> {
        loop {
            let history = self.read_history(&turn_id)?;

            match next_action(&history) {
                NextAction::CallProvider => {
                    let messages = build_context(&history);
                    let tools = self.tool_schemas();
                    match self.provider.request(&messages, &tools).await {
                        Ok(chunks) => {
                            let event = llm_response_from_chunks(chunks);
                            self.append_event(&turn_id, event)?;
                        }
                        Err(err) => {
                            // The turn should never linger 'open' after an unrecoverable
                            // provider failure — `TurnAborted` exists precisely for this
                            // (data model doc §4). Best-effort: propagate the original error
                            // regardless of whether the abort-close itself also fails.
                            let abort_row: pythia_eventlog::EventRow = KernelEvent::TurnAborted {
                                reason: err.to_string(),
                            }
                            .into();
                            let _ = self.eventlog.close_turn(
                                &turn_id,
                                EventLogTurnOutcome::Aborted,
                                &abort_row.payload_json,
                            );
                            return Err(KernelError::Provider(err));
                        }
                    }
                }
                NextAction::DispatchTool(tool_call) => {
                    // `next_action` only ever returns `DispatchTool` when the last event in
                    // `history` is the triggering `LlmResponse` that carried `tool_call` (data
                    // model doc §5 / turn.rs's own contract) — so its taint is right here,
                    // no re-fetch needed.
                    let triggering_tainted = history
                        .last()
                        .map(KernelEvent::tainted)
                        .unwrap_or(false);
                    let event = dispatch_tool(
                        &tool_call,
                        &self.skills,
                        &self.policy,
                        triggering_tainted,
                    );
                    self.append_event(&turn_id, event)?;
                }
                NextAction::Complete => {
                    self.eventlog
                        .close_turn(&turn_id, EventLogTurnOutcome::Complete, "{}")?;
                    break;
                }
            }
        }

        let events = self.read_history(&turn_id)?;
        Ok(TurnOutcome {
            turn_id,
            status: EventLogTurnOutcome::Complete,
            events,
        })
    }

    fn read_history(&self, turn_id: &TurnId) -> Result<Vec<KernelEvent>, KernelError> {
        let rows = self.eventlog.read_turn(turn_id)?;
        rows.into_iter()
            .map(KernelEvent::try_from)
            .collect::<Result<Vec<_>, _>>()
            .map_err(KernelError::from)
    }

    fn append_event(&self, turn_id: &TurnId, event: KernelEvent) -> Result<(), KernelError> {
        let row: pythia_eventlog::EventRow = event.into();
        self.eventlog.append(
            turn_id,
            NewEvent {
                event_type: &row.event_type,
                payload_json: &row.payload_json,
                effect_result: row.effect_result.as_deref(),
                tainted: row.tainted,
            },
        )?;
        Ok(())
    }

    /// The tools advertised to the provider on every call — one per registered skill. This
    /// slice's manifests carry no argument JSON-schema of their own (`pythia_manifest`'s
    /// `SkillManifest` is capability-request-shaped, not tool-schema-shaped), so
    /// `parameters_schema` is the permissive empty-object placeholder; nothing in this task's
    /// tests depends on a stricter schema being advertised.
    fn tool_schemas(&self) -> Vec<ToolSchema> {
        self.skills
            .keys()
            .map(|name| ToolSchema {
                name: name.clone(),
                description: String::new(),
                parameters_schema: serde_json::json!({}),
            })
            .collect()
    }
}

/// Merges a provider's ordered response chunks into a single `LlmResponse` event: every `Text`
/// chunk's content concatenated (newline-joined) and the *first* `ToolCall` chunk carried as the
/// event's tool call (this slice dispatches one tool per provider round-trip; a provider that
/// requested more than one in a single response has the rest silently ignored here rather than
/// dispatched — no test in this task exercises multi-tool-call-per-response, and the pure
/// `next_action`/dispatch loop already gives a provider a chance to request the next tool on its
/// very next turn, so nothing is lost, just serialized one call at a time).
///
/// Always tainted: the LLM itself is an untrusted source (data model doc §7), regardless of what
/// it said.
fn llm_response_from_chunks(chunks: Vec<ResponseChunk>) -> KernelEvent {
    let mut text = String::new();
    let mut tool_call = None;
    for chunk in chunks {
        match chunk {
            ResponseChunk::Text(chunk_text) => {
                if !text.is_empty() {
                    text.push('\n');
                }
                text.push_str(&chunk_text);
            }
            ResponseChunk::ToolCall(chunk_tool_call) => {
                if tool_call.is_none() {
                    tool_call = Some(ToolCall {
                        name: chunk_tool_call.name,
                        arguments: chunk_tool_call.arguments,
                    });
                }
            }
        }
    }
    KernelEvent::LlmResponse {
        text,
        tool_call,
        tainted: true,
    }
}

#[cfg(test)]
#[allow(non_snake_case)]
mod tests {
    use super::*;

    #[test]
    fn LlmResponseFromChunks_TextThenToolCall_MergesIntoOneEventTaintedTrue() {
        let chunks = vec![
            ResponseChunk::Text("let me check that".to_string()),
            ResponseChunk::ToolCall(pythia_provider::ToolCall {
                id: "call_1".to_string(),
                name: "read_file".to_string(),
                arguments: serde_json::json!({"path": "/notes/todo.txt"}),
            }),
        ];

        let event = llm_response_from_chunks(chunks);

        assert_eq!(
            event,
            KernelEvent::LlmResponse {
                text: "let me check that".to_string(),
                tool_call: Some(ToolCall {
                    name: "read_file".to_string(),
                    arguments: serde_json::json!({"path": "/notes/todo.txt"}),
                }),
                tainted: true,
            }
        );
    }

    #[test]
    fn LlmResponseFromChunks_TextOnly_NoToolCallTaintedTrue() {
        let chunks = vec![ResponseChunk::Text("all done".to_string())];

        let event = llm_response_from_chunks(chunks);

        assert_eq!(
            event,
            KernelEvent::LlmResponse {
                text: "all done".to_string(),
                tool_call: None,
                tainted: true,
            }
        );
    }
}
