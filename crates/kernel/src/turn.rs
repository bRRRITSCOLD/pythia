//! The pure decision function at the heart of the durability guarantee (Task 15; data model doc
//! §5's resume algorithm, implemented verbatim).
//!
//! `next_action` derives the single next step purely from the *shape of the last event* in a
//! turn's history — no hidden kernel state, no scanning further back than the tail (ADR-0002).
//! This is what makes `Kernel::resume` a pure read: replaying a truncated log and calling
//! `next_action` on it produces exactly the same decision a live, never-crashed turn would have
//! made at that same point, because the function has no side channel to have drifted through.

use crate::event::{KernelEvent, ToolCall};

/// The single next step the turn loop should take, decided purely from a turn's event history.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum NextAction {
    /// Call the provider with the full (compacted) turn history as context.
    CallProvider,
    /// Dispatch this tool call through the capability host — the last event is an `LlmResponse`
    /// carrying a tool call with no `ToolResult` recorded for it yet.
    DispatchTool(ToolCall),
    /// Nothing left to do: close the turn.
    Complete,
}

/// Data model doc §5, verbatim: walk to the last recorded event and decide from its shape alone.
///
/// - No history (a turn that hasn't even opened yet) → `CallProvider` is the wrong signal to
///   give a caller that shouldn't be running `next_action` at all yet, but this function stays
///   total rather than partial: an empty history can only arise from a caller bug (a turn is
///   opened by inserting its `UserCommand` atomically, per data model §6), and treating it as
///   "nothing to dispatch, ask the provider" is the safe default rather than a reachable panic.
/// - Last event `UserCommand` → the turn just opened, nothing has been sent to the provider yet.
/// - Last event `LlmResponse` carrying a tool call → that call has not yet produced a
///   `ToolResult` (if it had, *that* `ToolResult` would be the last event instead) → dispatch it.
/// - Last event `LlmResponse` carrying no tool call → the provider gave a final answer → the turn
///   is done.
/// - Last event `ToolResult` → the effect is a recorded fact; call the provider again with the
///   now-extended context, per data model §5 point 2's worked example.
/// - Last event already a terminal marker (`TurnComplete`/`TurnAborted`) → nothing to do; this
///   should be unreachable in practice (a terminal event and `turns.status = 'open'` never
///   coexist — data model §6's atomic turn-close), kept total rather than partial for the same
///   reason as the empty-history case above.
pub(crate) fn next_action(history: &[KernelEvent]) -> NextAction {
    match history.last() {
        None => NextAction::CallProvider,
        Some(KernelEvent::UserCommand { .. }) => NextAction::CallProvider,
        Some(KernelEvent::ToolResult { .. }) => NextAction::CallProvider,
        Some(KernelEvent::LlmResponse {
            tool_call: Some(tool_call),
            ..
        }) => NextAction::DispatchTool(tool_call.clone()),
        Some(KernelEvent::LlmResponse {
            tool_call: None, ..
        }) => NextAction::Complete,
        Some(KernelEvent::TurnComplete) | Some(KernelEvent::TurnAborted { .. }) => {
            NextAction::Complete
        }
    }
}

#[cfg(test)]
#[allow(non_snake_case)]
mod tests {
    use super::*;

    fn user_command(text: &str) -> KernelEvent {
        KernelEvent::UserCommand {
            text: text.to_string(),
            tainted: false,
        }
    }

    fn llm_response_with_tool(name: &str) -> KernelEvent {
        KernelEvent::LlmResponse {
            text: String::new(),
            tool_call: Some(ToolCall {
                name: name.to_string(),
                arguments: serde_json::json!({"path": "/notes/todo.txt"}),
            }),
            tainted: true,
        }
    }

    fn llm_response_text_only(text: &str) -> KernelEvent {
        KernelEvent::LlmResponse {
            text: text.to_string(),
            tool_call: None,
            tainted: true,
        }
    }

    fn tool_result(tool: &str) -> KernelEvent {
        KernelEvent::ToolResult {
            tool: tool.to_string(),
            status: "ok".to_string(),
            output: "file contents".to_string(),
            reason: None,
            tainted: true,
        }
    }

    #[test]
    fn NextAction_LastEventUserCommand_CallProvider() {
        let history = vec![user_command("summarize my notes")];

        assert_eq!(next_action(&history), NextAction::CallProvider);
    }

    #[test]
    fn NextAction_LastEventLlmResponseWithUncalledToolCall_DispatchThatTool() {
        let history = vec![
            user_command("summarize my notes"),
            llm_response_with_tool("read_file"),
        ];

        assert_eq!(
            next_action(&history),
            NextAction::DispatchTool(ToolCall {
                name: "read_file".to_string(),
                arguments: serde_json::json!({"path": "/notes/todo.txt"}),
            })
        );
    }

    #[test]
    fn NextAction_LastEventToolResult_CallProviderAgainWithExtendedContext() {
        let history = vec![
            user_command("summarize my notes"),
            llm_response_with_tool("read_file"),
            tool_result("read_file"),
        ];

        assert_eq!(next_action(&history), NextAction::CallProvider);
    }

    #[test]
    fn NextAction_LastEventLlmResponseNoToolCall_Complete() {
        let history = vec![
            user_command("summarize my notes"),
            llm_response_with_tool("read_file"),
            tool_result("read_file"),
            llm_response_text_only("here is your summary"),
        ];

        assert_eq!(next_action(&history), NextAction::Complete);
    }

    #[test]
    fn NextAction_EmptyHistory_CallProviderNotPanic() {
        assert_eq!(next_action(&[]), NextAction::CallProvider);
    }

    #[test]
    fn NextAction_LastEventTurnComplete_Complete() {
        let history = vec![user_command("hi"), KernelEvent::TurnComplete];

        assert_eq!(next_action(&history), NextAction::Complete);
    }

    #[test]
    fn NextAction_LastEventTurnAborted_Complete() {
        let history = vec![
            user_command("hi"),
            KernelEvent::TurnAborted {
                reason: "provider unreachable".to_string(),
            },
        ];

        assert_eq!(next_action(&history), NextAction::Complete);
    }
}
