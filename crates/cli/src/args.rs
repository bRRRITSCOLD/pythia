//! Command parsing: the one CLI command shape this slice needs, `pythia run "<text>"`.
//!
//! Resume-on-startup is deliberately *not* a flag here (per the plan's Task 16 file-level
//! approach) — it is unconditional composition-root behavior (`execute` in `lib.rs`), matching
//! the kernel's own "resume is just the normal loop" design (ADR-0002). This module only parses
//! the shape of *new* input.

/// The one command shape this slice's CLI understands.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Command {
    /// `pythia run "<text>"` — open a new turn with `text` as the user's command.
    Run { text: String },
}

/// Parsing failures. Distinguished by variant (rather than a single opaque string) so a caller
/// that wants to react differently to "nothing was typed" vs. "unrecognized command" can, even
/// though this slice's own `run()` treats all of them the same way (print and exit non-zero).
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum ArgsError {
    #[error("no command given (expected: pythia run \"<text>\")")]
    MissingCommand,
    #[error("unknown command `{0}` (expected: run)")]
    UnknownCommand(String),
    #[error("`run` requires user text (expected: pythia run \"<text>\")")]
    MissingRunText,
}

/// Parses `args` — the command-line arguments *after* the program name (i.e. `["run", "do the
/// thing"]`, not `["pythia", "run", "do the thing"]`) — into a [`Command`]. Never panics: a
/// missing or unrecognized command is a data error the caller renders and exits on, not a crash
/// — this is the process's single external input surface, and it is untrusted input by
/// construction (data model doc §7's taint boundary starts here).
pub fn parse(args: &[String]) -> Result<Command, ArgsError> {
    let mut iter = args.iter();
    let command_name = iter.next().ok_or(ArgsError::MissingCommand)?;

    match command_name.as_str() {
        "run" => {
            let text = iter.next().ok_or(ArgsError::MissingRunText)?;
            Ok(Command::Run { text: text.clone() })
        }
        other => Err(ArgsError::UnknownCommand(other.to_string())),
    }
}

#[cfg(test)]
#[allow(non_snake_case)]
mod tests {
    use super::*;

    #[test]
    fn Cli_ParseRunCommand_ExtractsUserText() {
        let args = vec!["run".to_string(), "summarize notes.txt".to_string()];

        let command = parse(&args).expect("valid `run` command must parse");

        assert_eq!(
            command,
            Command::Run {
                text: "summarize notes.txt".to_string()
            }
        );
    }

    #[test]
    fn Parse_NoArgs_ErrorsMissingCommandNotPanic() {
        let result = parse(&[]);

        assert_eq!(result, Err(ArgsError::MissingCommand));
    }

    #[test]
    fn Parse_UnknownCommand_ErrorsNotPanic() {
        let args = vec!["fly-to-the-moon".to_string()];

        let result = parse(&args);

        assert_eq!(
            result,
            Err(ArgsError::UnknownCommand("fly-to-the-moon".to_string()))
        );
    }

    #[test]
    fn Parse_RunWithoutText_ErrorsMissingRunTextNotPanic() {
        let args = vec!["run".to_string()];

        let result = parse(&args);

        assert_eq!(result, Err(ArgsError::MissingRunText));
    }
}
