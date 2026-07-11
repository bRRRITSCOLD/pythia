//! `pythia-cli`: the single input surface and the only crate that knows every concrete type
//! (architecture doc §2). Wires `OllamaProvider`, a SQLite path, and a policy file into a
//! `Kernel` (`compose.rs`), parses the one command shape this slice needs (`args.rs`), and
//! renders results to stdout (`render.rs`).
//!
//! `run(args) -> ExitCode` is a library entry point on purpose (not folded into `main.rs`) so
//! Tasks 17/18's integration tests can drive it in-process without shelling out to a built
//! binary — `main.rs` stays a thin `parse → run() → ExitCode` wrapper.

pub mod args;
pub mod compose;
pub mod render;

pub use args::{parse, ArgsError, Command};
pub use compose::{build_kernel, ComposeError, Config};

use std::io::Write;
use std::process::ExitCode;

use pythia_kernel::{Kernel, KernelError, TurnOutcome};
use pythia_provider::Provider;

/// Startup + single-command execution against an already-composed `Kernel`. Generic over
/// `Provider` specifically so this — the behavior the plan's Task 16 tests target — is testable
/// against a `MockProvider` without a live Ollama server; `run()` below is the only caller that
/// supplies the real `OllamaProvider`.
///
/// Checks for an open turn and resumes it (`Kernel::resume`) *before* running `command` — the
/// durability guarantee's user-facing surface (data model doc §5's resume algorithm): a crash
/// mid-turn is invisible to the next invocation except that it gets finished first, not silently
/// abandoned in favor of whatever new thing the user just typed.
pub async fn execute<P: Provider>(
    kernel: &mut Kernel<P>,
    command: Command,
) -> Result<Vec<TurnOutcome>, KernelError> {
    let mut outcomes = Vec::new();

    if let Some(resumed) = kernel.resume().await? {
        outcomes.push(resumed);
    }

    match command {
        Command::Run { text } => {
            outcomes.push(kernel.run_turn(text).await?);
        }
    }

    Ok(outcomes)
}

/// The library entry point: `main.rs`'s thin binary and Tasks 17/18's integration tests both
/// call this. `args` is the full process argv, program name included at `args[0]` (i.e.
/// `std::env::args().collect()`), matching the convention every caller already has on hand.
pub async fn run(args: Vec<String>) -> ExitCode {
    let command = match args::parse(args.get(1..).unwrap_or_default()) {
        Ok(command) => command,
        Err(err) => {
            eprintln!("{err}");
            return ExitCode::FAILURE;
        }
    };

    let config = Config::from_env();
    let mut kernel = match build_kernel(&config) {
        Ok(kernel) => kernel,
        Err(err) => {
            eprintln!("{err}");
            return ExitCode::FAILURE;
        }
    };

    match execute(&mut kernel, command).await {
        Ok(outcomes) => {
            let stdout = std::io::stdout();
            let mut lock = stdout.lock();
            for outcome in &outcomes {
                if render::render_turn_outcome(outcome, &mut lock).is_err() {
                    return ExitCode::FAILURE;
                }
            }
            let _ = lock.flush();
            ExitCode::SUCCESS
        }
        Err(err) => {
            eprintln!("{err}");
            ExitCode::FAILURE
        }
    }
}

#[cfg(test)]
#[allow(non_snake_case)]
mod tests {
    use super::*;

    use std::collections::HashMap;

    use pythia_eventlog::EventLog;
    use pythia_manifest::PolicyFile;
    use pythia_provider::mock::{MockProvider, ScriptedResponse};

    /// Builds a `Kernel<MockProvider>` over a fresh in-memory event log — the injected-provider
    /// seam every test in this module uses instead of a live Ollama server (design note in the
    /// task: merge-gate tests must not require live Ollama).
    fn mock_kernel(scripted: Vec<ScriptedResponse>) -> Kernel<MockProvider> {
        let eventlog = EventLog::open_in_memory().expect("in-memory event log opens");
        let provider = MockProvider::new(scripted);
        Kernel::new(eventlog, provider, PolicyFile::default(), HashMap::new())
    }

    #[tokio::test]
    async fn Cli_StartupNoOpenTurn_AcceptsNewCommand() {
        let mut kernel = mock_kernel(vec![ScriptedResponse::text("hi there")]);

        let outcomes = execute(
            &mut kernel,
            Command::Run {
                text: "new input".to_string(),
            },
        )
        .await
        .expect("execute must not error");

        assert_eq!(
            outcomes.len(),
            1,
            "no open turn to resume: exactly the new command's outcome"
        );
        let new_turn = &outcomes[0];
        assert!(new_turn
            .events
            .iter()
            .any(|event| matches!(event, pythia_kernel::KernelEvent::UserCommand { text, .. } if text == "new input")));
    }

    #[tokio::test]
    async fn Cli_StartupWithOpenTurn_InvokesResumeBeforeAcceptingNewInput() {
        let mut eventlog = EventLog::open_in_memory().expect("in-memory event log opens");
        // Simulate a crash-abandoned turn: a `turns` row + opening `UserCommand` event with no
        // terminal event, left by a previous (never-completed) process, exactly what
        // `EventLog::find_open_turn` is built to find (data model doc §5).
        let abandoned_turn_id = eventlog
            .open_turn(r#"{"text":"resume this"}"#, false)
            .expect("opening an abandoned turn for the fixture must not error");

        let provider = MockProvider::new(vec![
            ScriptedResponse::text("resumed reply"),
            ScriptedResponse::text("new reply"),
        ]);
        let mut kernel = Kernel::new(eventlog, provider, PolicyFile::default(), HashMap::new());

        let outcomes = execute(
            &mut kernel,
            Command::Run {
                text: "new input".to_string(),
            },
        )
        .await
        .expect("execute must not error");

        assert_eq!(
            outcomes.len(),
            2,
            "expected the resumed turn's outcome followed by the new command's outcome"
        );
        assert_eq!(
            outcomes[0].turn_id, abandoned_turn_id,
            "the first outcome must be the resumed (previously open) turn, not the new one"
        );
        assert_ne!(
            outcomes[1].turn_id, abandoned_turn_id,
            "the second outcome must be a distinct, newly-opened turn"
        );
        assert!(
            outcomes[1]
                .events
                .iter()
                .any(|event| matches!(event, pythia_kernel::KernelEvent::UserCommand { text, .. } if text == "new input")),
            "the new command's text must appear only in the second (new) turn"
        );
    }
}
