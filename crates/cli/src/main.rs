//! `pythia`: thin binary entry point. All logic lives in `pythia_cli::run` (`src/lib.rs`) so it
//! stays testable in-process (Tasks 17/18's integration tests drive `run()` directly rather than
//! shelling out to this binary) — this file does nothing but collect argv, run the async
//! composition root on a single-threaded runtime (the workload is one turn at a time — plan §0),
//! and translate the result into a process exit code.

use std::process::ExitCode;

#[tokio::main(flavor = "current_thread")]
async fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().collect();
    pythia_cli::run(args).await
}
