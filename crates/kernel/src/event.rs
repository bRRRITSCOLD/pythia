//! The kernel's own typed event vocabulary (`KernelEvent`) and its pure translation to/from
//! `pythia_eventlog::EventRow` — the generic envelope the event log actually stores.
//!
//! This module does no I/O. `pythia-eventlog` knows nothing about `UserCommand`/`LlmResponse`/
//! etc.; this is the one place that vocabulary is defined and mapped onto the generic
//! `{type, payload_json, effect_result, tainted}` envelope (data model doc §0, §4).

use pythia_eventlog::{EventRow, TurnId};
use serde::{Deserialize, Serialize};

/// A tool invocation an `LlmResponse` may carry. Deliberately local to `pythia-kernel` rather
/// than reused from `pythia-provider`'s wire-agnostic `ToolCall` — this module's only locked
/// dependency is Task 3 (`pythia-eventlog`); coupling the event vocabulary to the provider
/// crate's evolving wire type would widen that dependency for no behavior this crate needs
/// (`arguments` here is exactly the shape the kernel persists and later replays, no more).
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ToolCall {
    pub name: String,
    pub arguments: serde_json::Value,
}

/// The kernel's typed event vocabulary — the five shapes named in the data model's `events.type`
/// CHECK constraint (data model doc §4), given real fields instead of an opaque JSON blob.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum KernelEvent {
    /// Turn input, kernel-authored from the CLI channel.
    UserCommand { text: String },
    /// Provider output: assistant text and/or a tool-call request.
    LlmResponse {
        text: String,
        tool_call: Option<ToolCall>,
    },
    /// The effect: a completed tool/skill invocation, or a policy denial (data model doc §4 —
    /// denials are recorded as a `ToolResult` with `status: "denied"`, not a separate type).
    ToolResult {
        tool: String,
        status: String,
        output: String,
        tainted: bool,
    },
    /// Terminal marker, normal end.
    TurnComplete,
    /// Terminal marker, abnormal end (crash-abandoned / hard error).
    TurnAborted { reason: String },
}

impl KernelEvent {
    /// The `events.type` string this variant maps to — the CHECK constraint's own vocabulary.
    pub fn event_type(&self) -> &'static str {
        match self {
            KernelEvent::UserCommand { .. } => "UserCommand",
            KernelEvent::LlmResponse { .. } => "LlmResponse",
            KernelEvent::ToolResult { .. } => "ToolResult",
            KernelEvent::TurnComplete => "TurnComplete",
            KernelEvent::TurnAborted { .. } => "TurnAborted",
        }
    }

    /// Whether this event should be recorded with `events.tainted = 1`. Only a `ToolResult` can
    /// be tainted in this vocabulary — the ingestion-time invariant is a manifest-declared
    /// property of the tool that produced it, not something this translation layer infers from
    /// content (data model doc §7).
    pub fn tainted(&self) -> bool {
        matches!(self, KernelEvent::ToolResult { tainted, .. } if *tainted)
    }
}

/// Errors surfaced translating a generic `EventRow` back into a `KernelEvent`. The database's
/// own CHECK constraints already rule most of these out in practice, but this layer doesn't get
/// to assume the constraint is the only thing standing between it and bad data (e.g. rows read
/// from an older/newer schema version, or constructed directly in a test).
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum TranslateError {
    #[error("unknown event type: {0}")]
    UnknownEventType(String),
    #[error("malformed {event_type} payload_json: {reason}")]
    MalformedPayload { event_type: String, reason: String },
    #[error("malformed {event_type} effect_result: {reason}")]
    MalformedEffectResult { event_type: String, reason: String },
    #[error("ToolResult row is missing effect_result")]
    MissingEffectResult,
}

// ---- payload_json wire shapes, private to this module ----

#[derive(Serialize, Deserialize)]
struct UserCommandPayload {
    text: String,
}

#[derive(Serialize, Deserialize)]
struct LlmResponsePayload {
    text: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    tool_call: Option<ToolCall>,
}

#[derive(Serialize, Deserialize)]
struct ToolResultPayload {
    tool: String,
}

#[derive(Serialize, Deserialize)]
struct ToolResultEffect {
    status: String,
    output: String,
}

#[derive(Serialize, Deserialize)]
struct TurnAbortedPayload {
    reason: String,
}

/// Translate a `KernelEvent` into the generic envelope `pythia-eventlog` stores.
///
/// `turn_id`, `seq`, and `created` are not part of a `KernelEvent`'s own identity (they are
/// assigned by the event log on insert / read), so this direction fills them with placeholder
/// values (`turn_id` empty, `seq = 0`, `created` empty). Callers that persist a `KernelEvent`
/// (the kernel's turn loop, Task 15) read only `event_type`/`payload_json`/`effect_result`/
/// `tainted` off the result and pass the real `turn_id` to `EventLog::append` themselves — this
/// impl exists so the translation logic is exercised and round-tripped as pure data here, ahead
/// of any I/O.
impl From<KernelEvent> for EventRow {
    fn from(event: KernelEvent) -> Self {
        let event_type = event.event_type().to_string();
        let tainted = event.tainted();

        let (payload_json, effect_result) = match event {
            KernelEvent::UserCommand { text } => {
                let payload = serde_json::to_string(&UserCommandPayload { text })
                    .expect("UserCommandPayload always serializes");
                (payload, None)
            }
            KernelEvent::LlmResponse { text, tool_call } => {
                let payload = serde_json::to_string(&LlmResponsePayload { text, tool_call })
                    .expect("LlmResponsePayload always serializes");
                (payload, None)
            }
            KernelEvent::ToolResult {
                tool,
                status,
                output,
                tainted: _,
            } => {
                let payload = serde_json::to_string(&ToolResultPayload { tool })
                    .expect("ToolResultPayload always serializes");
                let effect = serde_json::to_string(&ToolResultEffect { status, output })
                    .expect("ToolResultEffect always serializes");
                (payload, Some(effect))
            }
            KernelEvent::TurnComplete => ("{}".to_string(), None),
            KernelEvent::TurnAborted { reason } => {
                let payload = serde_json::to_string(&TurnAbortedPayload { reason })
                    .expect("TurnAbortedPayload always serializes");
                (payload, None)
            }
        };

        EventRow {
            seq: 0,
            turn_id: TurnId::from(String::new()),
            event_type,
            payload_json,
            effect_result,
            tainted,
            created: String::new(),
        }
    }
}

impl TryFrom<EventRow> for KernelEvent {
    type Error = TranslateError;

    fn try_from(row: EventRow) -> Result<Self, Self::Error> {
        match row.event_type.as_str() {
            "UserCommand" => {
                let payload: UserCommandPayload =
                    serde_json::from_str(&row.payload_json).map_err(|e| {
                        TranslateError::MalformedPayload {
                            event_type: row.event_type.clone(),
                            reason: e.to_string(),
                        }
                    })?;
                Ok(KernelEvent::UserCommand { text: payload.text })
            }
            "LlmResponse" => {
                let payload: LlmResponsePayload =
                    serde_json::from_str(&row.payload_json).map_err(|e| {
                        TranslateError::MalformedPayload {
                            event_type: row.event_type.clone(),
                            reason: e.to_string(),
                        }
                    })?;
                Ok(KernelEvent::LlmResponse {
                    text: payload.text,
                    tool_call: payload.tool_call,
                })
            }
            "ToolResult" => {
                let payload: ToolResultPayload =
                    serde_json::from_str(&row.payload_json).map_err(|e| {
                        TranslateError::MalformedPayload {
                            event_type: row.event_type.clone(),
                            reason: e.to_string(),
                        }
                    })?;
                let effect_json = row
                    .effect_result
                    .as_deref()
                    .ok_or(TranslateError::MissingEffectResult)?;
                let effect: ToolResultEffect = serde_json::from_str(effect_json).map_err(|e| {
                    TranslateError::MalformedEffectResult {
                        event_type: row.event_type.clone(),
                        reason: e.to_string(),
                    }
                })?;
                Ok(KernelEvent::ToolResult {
                    tool: payload.tool,
                    status: effect.status,
                    output: effect.output,
                    tainted: row.tainted,
                })
            }
            "TurnComplete" => Ok(KernelEvent::TurnComplete),
            "TurnAborted" => {
                let payload: TurnAbortedPayload =
                    serde_json::from_str(&row.payload_json).map_err(|e| {
                        TranslateError::MalformedPayload {
                            event_type: row.event_type.clone(),
                            reason: e.to_string(),
                        }
                    })?;
                Ok(KernelEvent::TurnAborted {
                    reason: payload.reason,
                })
            }
            other => Err(TranslateError::UnknownEventType(other.to_string())),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn round_trip(event: KernelEvent) -> KernelEvent {
        let row: EventRow = event.into();
        KernelEvent::try_from(row).expect("round trip must not error")
    }

    #[test]
    fn translate_user_command_round_trips_through_event_row() {
        let event = KernelEvent::UserCommand {
            text: "hello there".to_string(),
        };

        assert_eq!(round_trip(event.clone()), event);
    }

    #[test]
    fn translate_llm_response_with_tool_call_round_trips() {
        let event = KernelEvent::LlmResponse {
            text: "let me check that".to_string(),
            tool_call: Some(ToolCall {
                name: "read_file".to_string(),
                arguments: serde_json::json!({"path": "/notes/todo.txt"}),
            }),
        };

        assert_eq!(round_trip(event.clone()), event);
    }

    #[test]
    fn translate_llm_response_text_only_round_trips() {
        let event = KernelEvent::LlmResponse {
            text: "all done".to_string(),
            tool_call: None,
        };

        let row: EventRow = event.clone().into();
        // no tool call means no key at all in the wire payload, not a null placeholder.
        assert!(!row.payload_json.contains("tool_call"));
        assert_eq!(KernelEvent::try_from(row).unwrap(), event);
    }

    #[test]
    fn translate_tool_result_preserves_tainted_flag() {
        let tainted_event = KernelEvent::ToolResult {
            tool: "read_file".to_string(),
            status: "ok".to_string(),
            output: "file contents".to_string(),
            tainted: true,
        };
        let clean_event = KernelEvent::ToolResult {
            tool: "read_file".to_string(),
            status: "ok".to_string(),
            output: "file contents".to_string(),
            tainted: false,
        };

        let tainted_row: EventRow = tainted_event.clone().into();
        assert!(tainted_row.tainted);
        assert_eq!(
            round_trip(tainted_event),
            KernelEvent::try_from(tainted_row).unwrap()
        );

        let clean_row: EventRow = clean_event.clone().into();
        assert!(!clean_row.tainted);
        assert_eq!(
            round_trip(clean_event),
            KernelEvent::try_from(clean_row).unwrap()
        );
    }

    #[test]
    fn translate_tool_result_denied_preserves_status_in_effect_result() {
        let event = KernelEvent::ToolResult {
            tool: "send_email".to_string(),
            status: "denied".to_string(),
            output: String::new(),
            tainted: false,
        };

        let row: EventRow = event.clone().into();
        let effect_result = row
            .effect_result
            .clone()
            .expect("ToolResult carries an effect_result");
        assert!(effect_result.contains("\"status\":\"denied\""));

        assert_eq!(round_trip(event), KernelEvent::try_from(row).unwrap());
    }

    #[test]
    fn try_from_event_row_with_unknown_type_errors_not_panics() {
        let row = EventRow {
            seq: 1,
            turn_id: TurnId::from("t1".to_string()),
            event_type: "Bogus".to_string(),
            payload_json: "{}".to_string(),
            effect_result: None,
            tainted: false,
            created: "2026-07-10T00:00:00.000Z".to_string(),
        };

        let result = KernelEvent::try_from(row);

        assert_eq!(
            result,
            Err(TranslateError::UnknownEventType("Bogus".to_string()))
        );
    }

    #[test]
    fn try_from_tool_result_row_missing_effect_result_errors_not_panics() {
        let row = EventRow {
            seq: 1,
            turn_id: TurnId::from("t1".to_string()),
            event_type: "ToolResult".to_string(),
            payload_json: serde_json::to_string(&ToolResultPayload {
                tool: "read_file".to_string(),
            })
            .unwrap(),
            effect_result: None,
            tainted: false,
            created: "2026-07-10T00:00:00.000Z".to_string(),
        };

        let result = KernelEvent::try_from(row);

        assert_eq!(result, Err(TranslateError::MissingEffectResult));
    }
}
