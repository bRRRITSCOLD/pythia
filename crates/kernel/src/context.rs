//! Context-window compaction (Task 15; plan §0's KISS call): "send the whole turn's events" —
//! the simplest algorithm that satisfies the architecture's fixed mechanism (the kernel rebuilds
//! provider context from the log on every call, never from in-memory state). This slice
//! deliberately does *not* summarize or drop anything; a future real compaction algorithm (spec
//! §8, deferred past the slice) replaces only this function.
//!
//! # Known deferral carried forward from `pythia-provider` (not resolved here)
//!
//! `pythia_provider::Message` is deliberately minimal (`role` + `content: String`) and does not
//! yet carry a structured `tool_calls`/`tool_call_id` correlation (see that crate's own doc
//! comment on `Message`, which flagged this as "expected before Task 15 lands"). Extending that
//! wire-agnostic type would ripple into `pythia-provider`'s frozen contract-test suite and
//! `pythia-provider-ollama`'s wire translation — both already-merged, already-reviewed surfaces
//! outside this task's own crate. None of Task 15's required tests exercise OpenAI-dialect wire
//! fidelity for tool-call correlation (that's `MockProvider`'s job to stay agnostic to), so this
//! function takes the smaller, in-scope KISS path instead: a tool call/result is rendered as
//! plain text appended to the message content. This keeps `build_context` fully testable and
//! correct for what Task 15 actually needs (full, undropped history reaching the provider) without
//! touching crates whose contracts belong to already-closed tasks. Flagged here, not hidden, for
//! whoever eventually gives a real provider strict tool-call-id wire fidelity.

use pythia_provider::{Message, Role};

use crate::event::KernelEvent;

/// Rebuilds the full provider context from `history` — one `Message` per event, in order, with
/// nothing dropped and nothing summarized (plan §0's locked-in slice algorithm).
pub(crate) fn build_context(history: &[KernelEvent]) -> Vec<Message> {
    history.iter().map(event_to_message).collect()
}

fn event_to_message(event: &KernelEvent) -> Message {
    match event {
        KernelEvent::UserCommand { text, .. } => Message::user(text.clone()),
        KernelEvent::LlmResponse {
            text,
            tool_call: None,
            ..
        } => Message::assistant(text.clone()),
        KernelEvent::LlmResponse {
            text,
            tool_call: Some(tool_call),
            ..
        } => Message::new(
            Role::Assistant,
            format!(
                "{text}\n[tool_call name={} arguments={}]",
                tool_call.name, tool_call.arguments
            ),
        ),
        KernelEvent::ToolResult {
            tool,
            status,
            output,
            reason,
            ..
        } => {
            let reason_suffix = reason
                .as_deref()
                .map(|reason| format!(" reason={reason}"))
                .unwrap_or_default();
            Message::tool(format!(
                "tool={tool} status={status} output={output}{reason_suffix}"
            ))
        }
        KernelEvent::TurnComplete => Message::system("[turn complete]"),
        KernelEvent::TurnAborted { reason } => Message::system(format!("[turn aborted: {reason}]")),
    }
}

#[cfg(test)]
#[allow(non_snake_case)]
mod tests {
    use super::*;
    use crate::event::ToolCall;

    #[test]
    fn Compaction_SendsFullTurnHistory_NoEventsDropped() {
        let history = vec![
            KernelEvent::UserCommand {
                text: "summarize my notes.txt".to_string(),
                tainted: false,
            },
            KernelEvent::LlmResponse {
                text: "let me check that".to_string(),
                tool_call: Some(ToolCall {
                    name: "read_file".to_string(),
                    arguments: serde_json::json!({"path": "/notes/todo.txt"}),
                }),
                tainted: true,
            },
            KernelEvent::ToolResult {
                tool: "read_file".to_string(),
                status: "ok".to_string(),
                output: "buy milk".to_string(),
                reason: None,
                tainted: true,
            },
            KernelEvent::LlmResponse {
                text: "your notes say: buy milk".to_string(),
                tool_call: None,
                tainted: true,
            },
        ];

        let context = build_context(&history);

        // No summarization/truncation: every event in the turn's history produces exactly one
        // message — the locked-in "send the whole turn" KISS call (plan §0).
        assert_eq!(
            context.len(),
            history.len(),
            "expected one message per event, nothing dropped"
        );
        assert!(context[0].content.contains("summarize my notes.txt"));
        assert!(context[1].content.contains("read_file"));
        // The middle event's content (the tool's actual output) must survive to the final
        // message list — the property a summarizing/truncating algorithm would risk losing.
        assert!(context[2].content.contains("buy milk"));
        assert!(context[3].content.contains("your notes say: buy milk"));
    }

    #[test]
    fn BuildContext_EmptyHistory_ReturnsEmptyContext() {
        assert_eq!(build_context(&[]), Vec::new());
    }

    #[test]
    fn BuildContext_UserCommand_MapsToUserRoleMessage() {
        let history = vec![KernelEvent::UserCommand {
            text: "hello".to_string(),
            tainted: false,
        }];

        let context = build_context(&history);

        assert_eq!(context, vec![Message::user("hello")]);
    }
}
