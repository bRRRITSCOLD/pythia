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

/// Visible placeholder substituted for each stripped control/ANSI sequence. Chosen to be
/// unambiguous in a terminal (not itself a control character) and to make the fact of
/// stripping visible to the operator rather than silently disappearing.
const STRIPPED_PLACEHOLDER: &str = "\u{2426}"; // "SYMBOL FOR SUBSTITUTE" (␦-ish, printable)

/// Strips C0 control characters (except `\n`/`\t`, which are legitimate output formatting),
/// `0x7f` (DEL), and ANSI escape sequences (CSI `ESC [ ... final-byte` and OSC
/// `ESC ] ... (BEL | ESC \\)`) from `s`, replacing each stripped run with
/// [`STRIPPED_PLACEHOLDER`].
///
/// This is the SR-17 terminal-injection guard (`docs/superpowers/security/pythia-threat-model.md`):
/// tainted content (LLM output, tool output) can carry escape sequences that rewrite the
/// operator's terminal (clear screen, move the cursor, spoof a fake prompt). Applying this
/// before tainted text reaches stdout removes that capability while leaving ordinary printable
/// text — including the `<redacted:secret:...>` marker, which is plain ASCII — untouched.
fn sanitize_for_terminal(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut chars = s.chars().peekable();
    let mut stripped_pending = false;

    while let Some(c) = chars.next() {
        match c {
            '\n' | '\t' => {
                stripped_pending = false;
                out.push(c);
            }
            '\u{1b}' => {
                // ESC: consume a CSI (`[ ... final-byte`) or OSC (`] ... BEL|ESC \`) sequence,
                // or any other single escaped character, entirely.
                match chars.peek() {
                    Some('[') => {
                        chars.next();
                        for c2 in chars.by_ref() {
                            if ('\u{40}'..='\u{7e}').contains(&c2) {
                                break;
                            }
                        }
                    }
                    Some(']') => {
                        chars.next();
                        loop {
                            match chars.next() {
                                None | Some('\u{7}') => break,
                                Some('\u{1b}') if chars.peek() == Some(&'\\') => {
                                    chars.next();
                                    break;
                                }
                                _ => {}
                            }
                        }
                    }
                    Some(_) => {
                        chars.next();
                    }
                    None => {}
                }
                if !stripped_pending {
                    out.push_str(STRIPPED_PLACEHOLDER);
                    stripped_pending = true;
                }
            }
            c if c == '\u{7f}' || (c < '\u{20}') => {
                if !stripped_pending {
                    out.push_str(STRIPPED_PLACEHOLDER);
                    stripped_pending = true;
                }
            }
            c => {
                stripped_pending = false;
                out.push(c);
            }
        }
    }

    out
}

/// Renders one journalled event as a line (or a few) of human-readable stdout output.
/// `output`/`text` fields are written with the standard library's `Display` formatting only —
/// no parsing, no substring scanning, no "helpful" substitution of any marker found inside them —
/// except that content whose `tainted` flag is set is first passed through
/// [`sanitize_for_terminal`] to strip control/ANSI escape sequences (SR-17). Untainted,
/// kernel-authored content (e.g. the `> {text}` echo of what the operator just typed) renders
/// verbatim: it did not originate from an untrusted source, so there is nothing to sanitize
/// against.
pub fn render_event(event: &KernelEvent, out: &mut impl Write) -> io::Result<()> {
    match event {
        KernelEvent::UserCommand { text, .. } => writeln!(out, "> {text}"),
        KernelEvent::LlmResponse {
            text,
            tool_call,
            tainted,
        } => {
            if !text.is_empty() {
                let text = if *tainted {
                    sanitize_for_terminal(text)
                } else {
                    text.clone()
                };
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
            tainted,
        } => match reason {
            Some(reason) => {
                let reason = if *tainted {
                    sanitize_for_terminal(reason)
                } else {
                    reason.clone()
                };
                writeln!(out, "  [{tool}] {status}: {reason}")
            }
            None => {
                let output = if *tainted {
                    sanitize_for_terminal(output)
                } else {
                    output.clone()
                };
                writeln!(out, "  [{tool}] {status}: {output}")
            }
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
    fn Render_TaintedOutputWithAnsiEscapes_StrippedBeforeStdout() {
        // \x1b[2J (clear screen), \x1b[H (cursor home), and a raw NUL — a terminal-injection
        // payload an LLM or file-content-derived tool output could carry (SR-17).
        let payload = "\u{1b}[2J\u{1b}[Hpwned\0";
        let event = KernelEvent::ToolResult {
            tool: "read_file".to_string(),
            status: "ok".to_string(),
            output: payload.to_string(),
            reason: None,
            tainted: true,
        };

        let mut buf = Vec::new();
        render_event(&event, &mut buf).expect("render must not error");
        let rendered = String::from_utf8(buf).expect("render output is valid utf8");

        assert!(
            !rendered.contains('\u{1b}'),
            "raw ESC byte must not reach stdout, got: {rendered:?}"
        );
        assert!(
            !rendered.contains('\0'),
            "raw NUL byte must not reach stdout, got: {rendered:?}"
        );
        assert!(
            rendered.contains(STRIPPED_PLACEHOLDER),
            "expected the stripped-content placeholder in output, got: {rendered:?}"
        );
        assert!(
            rendered.contains("pwned"),
            "printable content around the escapes should survive, got: {rendered:?}"
        );
    }

    #[test]
    fn Render_UntaintedUserCommand_RenderedVerbatim() {
        // Policy (settled explicitly, per H11's "name it" instruction): only *tainted* content is
        // sanitized. `UserCommand` is kernel-authored from the CLI channel — the operator's own
        // keystrokes echoed back — so `tainted: false` here renders verbatim, escapes and all.
        // (A genuinely untrusted `UserCommand`, e.g. piped stdin from an untrusted source, would
        // be journalled with `tainted: true` and would go through the same sanitization path as
        // the tool/LLM arms above.)
        let event = KernelEvent::UserCommand {
            text: "echo \u{1b}[31mred\u{1b}[0m".to_string(),
            tainted: false,
        };

        let mut buf = Vec::new();
        render_event(&event, &mut buf).expect("render must not error");
        let rendered = String::from_utf8(buf).expect("render output is valid utf8");

        assert!(
            rendered.contains('\u{1b}'),
            "untainted content must render verbatim, escapes included, got: {rendered:?}"
        );
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
