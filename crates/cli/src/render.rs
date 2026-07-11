//! Turn output to stdout: renders exactly what `Kernel`/`ExecutionResult` hand it.
//!
//! SR-5's CLI-rendering clause (`docs/superpowers/security/pythia-threat-model.md`): no code
//! path here ever has access to a pre-redaction secret value. Redaction already happened inside
//! `pythia-capability-host::execute()` (Task 8) before a `ToolResult`'s `output` ever reaches
//! this crate — this module's only job is to print whatever string it was given, verbatim, never
//! attempting to "resolve" a `<redacted:secret:...>` marker back to a value. There is no lookup
//! table here that even could do that; this module doesn't hold secret material to begin with.

use std::io::{self, Write};

use pythia_kernel::{KernelEvent, TurnOutcome};

/// Renders one journalled event as a line (or a few) of human-readable stdout output.
/// `output`/`text` fields are written with the standard library's `Display` formatting only —
/// no parsing, no substring scanning, no "helpful" substitution of any marker found inside them.
pub fn render_event(event: &KernelEvent, out: &mut impl Write) -> io::Result<()> {
    match event {
        KernelEvent::UserCommand { text, .. } => writeln!(out, "> {text}"),
        KernelEvent::LlmResponse {
            text, tool_call, ..
        } => {
            if !text.is_empty() {
                writeln!(out, "{text}")?;
            }
            if let Some(tool_call) = tool_call {
                writeln!(out, "  [calling {}]", tool_call.name)?;
            }
            Ok(())
        }
        KernelEvent::ToolResult {
            tool,
            status,
            output,
            reason,
            ..
        } => match reason {
            Some(reason) => writeln!(out, "  [{tool}] {status}: {reason}"),
            None => writeln!(out, "  [{tool}] {status}: {output}"),
        },
        KernelEvent::TurnComplete => Ok(()),
        KernelEvent::TurnAborted { reason } => writeln!(out, "turn aborted: {reason}"),
    }
}

/// Renders every event in `outcome`, in journalled order, to `out`.
pub fn render_turn_outcome(outcome: &TurnOutcome, out: &mut impl Write) -> io::Result<()> {
    for event in &outcome.events {
        render_event(event, out)?;
    }
    Ok(())
}

#[cfg(test)]
#[allow(non_snake_case)]
mod tests {
    use super::*;
    use pythia_eventlog::{TurnId, TurnOutcome as EventLogTurnOutcome};

    #[test]
    fn Render_ToolResultContainingRedactionMarker_PrintsMarkerVerbatim() {
        // The exact marker shape `pythia-capability-host::execute`'s redaction pass produces
        // (`<redacted:secret:NAME>`) — already redacted by the time this layer ever sees it.
        let marker = "<redacted:secret:SMTP_PASSWORD>";
        let event = KernelEvent::ToolResult {
            tool: "send_email".to_string(),
            status: "ok".to_string(),
            output: format!("sent with credentials {marker}"),
            reason: None,
            tainted: true,
        };

        let mut buf = Vec::new();
        render_event(&event, &mut buf).expect("render must not error");
        let rendered = String::from_utf8(buf).expect("render output is valid utf8");

        assert!(
            rendered.contains(marker),
            "expected the redaction marker to be printed verbatim, got: {rendered:?}"
        );
        // No path in this module ever re-hydrates the marker back to a value — there's nothing
        // it could even substitute (this crate holds no secret material) — but the assertion
        // documents the invariant explicitly, at the one surface a future engineer might be
        // tempted to "helpfully resolve" for display.
        assert!(!rendered.contains("hunter2"));
    }

    #[test]
    fn Render_ToolResultOk_PrintsOutputVerbatim() {
        let event = KernelEvent::ToolResult {
            tool: "read_file".to_string(),
            status: "ok".to_string(),
            output: "buy milk".to_string(),
            reason: None,
            tainted: true,
        };

        let mut buf = Vec::new();
        render_event(&event, &mut buf).expect("render must not error");
        let rendered = String::from_utf8(buf).expect("render output is valid utf8");

        assert!(rendered.contains("buy milk"));
    }

    #[test]
    fn Render_TurnOutcome_RendersEveryEventInOrder() {
        let outcome = TurnOutcome {
            turn_id: TurnId::from("t1".to_string()),
            status: EventLogTurnOutcome::Complete,
            events: vec![
                KernelEvent::UserCommand {
                    text: "summarize notes.txt".to_string(),
                    tainted: false,
                },
                KernelEvent::LlmResponse {
                    text: "done".to_string(),
                    tool_call: None,
                    tainted: true,
                },
                KernelEvent::TurnComplete,
            ],
        };

        let mut buf = Vec::new();
        render_turn_outcome(&outcome, &mut buf).expect("render must not error");
        let rendered = String::from_utf8(buf).expect("render output is valid utf8");

        assert!(rendered.contains("summarize notes.txt"));
        assert!(rendered.contains("done"));
    }
}
