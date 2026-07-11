//! Integration test: the durability demo (Task 17, issue #17) — proves the spec's exit
//! criterion #1 (`docs/superpowers/specs/2026-07-10-pythia-engine-design.md` §6.1, data flow
//! §5's crash-resume worked example) end to end, not just at the kernel-unit level. Task 15's
//! `pythia-kernel::tests::turn_loop::Replay_TruncatedAtEachBoundary_ReExecutesNothing` already
//! proves the state machine by *seeding* a truncated log directly; this test instead drives a
//! real, live `Kernel` through `pythia_cli::execute()` — the same composition-root entry point
//! the CLI binary calls — until the real `read_file` effect (real wasmtime dispatch, real
//! capability-host `execute()`, real compiled `read-file.wasm`) has committed its `ToolResult`,
//! then genuinely interrupts the in-flight turn before it can continue, and proves a brand-new
//! `Kernel`/`EventLog` pair constructed over the same SQLite file resumes to completion without
//! re-executing anything already recorded.
//!
//! # Simulating "kill mid-turn" without a permanent production hook
//!
//! The plan's file-level approach (Task 17) describes an injected test-only breakpoint callback.
//! This suite achieves the same effect without adding any hook to `pythia-kernel`/`pythia-cli`
//! (kept out of scope here — this file and this crate's `Cargo.toml` dev-dependencies are the
//! only diffs this task should carry): the pre-crash `Kernel` is built over a `Provider` that
//! hangs forever (`std::future::pending`) on the *second* call it receives. Because
//! `Kernel::drive_turn`'s loop only reaches a second `CallProvider` step once the prior
//! `ToolResult` append has itself returned (the loop is single-threaded and fully synchronous
//! between `.await` points — see `pythia-kernel::turn::next_action`), the turn is provably
//! parked immediately *after* `E3: ToolResult{read_file}` has committed and before anything
//! else happens. The test then `tokio::spawn`s that turn onto a real OS thread and
//! `JoinHandle::abort()`s it — dropping the only live `Kernel`/`EventLog` handle uncleanly, with
//! no `close_turn` ever called, exactly like a real process crash.

#![allow(non_snake_case)]

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::process::Command as CargoCommand;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use pythia_cli::{execute, Command as CliCommand};
use pythia_eventlog::{EventLog, TurnOutcome as EventLogTurnOutcome};
use pythia_kernel::{Kernel, KernelEvent, SkillConfig, ToolCall};
use pythia_manifest::{Capability, Decision, PolicyFile, SkillManifest};
use pythia_provider::mock::{MockProvider, ScriptedResponse};
use pythia_provider::{
    Message, Provider, ProviderError, ResponseChunk, ToolCall as ProviderToolCall, ToolSchema,
};

/// Builds the real `read-file` skill for `wasm32-wasip1` — same approach as this repo's other
/// integration suites (`pythia-capability-host::tests::execute`, `pythia-kernel::dispatch` unit
/// tests, `pythia-kernel::tests::turn_loop`), duplicated locally per that established convention
/// rather than shared, so this demo exercises the actual durability-demo skill end to end, not a
/// synthetic WAT fixture.
fn build_read_file_wasm() -> Vec<u8> {
    let manifest_dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    let repo_root = manifest_dir
        .parent()
        .expect("crates/cli must have a parent (crates/)")
        .parent()
        .expect("crates/ must have a parent (the repo root)");
    let skills_workspace_dir = repo_root.join("skills");

    let status = CargoCommand::new(env!("CARGO"))
        .arg("build")
        .arg("--package")
        .arg("read-file")
        .arg("--target")
        .arg("wasm32-wasip1")
        .current_dir(&skills_workspace_dir)
        .status()
        .expect("failed to invoke cargo build for wasm32-wasip1 target");
    assert!(
        status.success(),
        "cargo build --package read-file --target wasm32-wasip1 must succeed"
    );

    let wasm_path = skills_workspace_dir
        .join("target")
        .join("wasm32-wasip1")
        .join("debug")
        .join("read-file.wasm");
    std::fs::read(&wasm_path).unwrap_or_else(|e| panic!("reading {wasm_path:?}: {e}"))
}

/// Number of events in this fixture's canonical, never-crashed run: `UserCommand`,
/// `LlmResponse{tool_call: read_file}`, `ToolResult{read_file}`, `LlmResponse{no tool_call}`,
/// `TurnComplete` — the single-tool-call analog of spec §5's E1..E6 worked example (that example
/// adds a `send_email` round-trip Task 17's own file-level approach doesn't exercise — this demo
/// scripts `read_file` then a turn-ending text completion, nothing else), matching
/// `pythia-kernel::tests::turn_loop`'s own fixture shape and its documented rationale exactly.
const CANONICAL_LEN: usize = 5;

/// Wires the `read_file` skill (granted over `granted_dir`) and returns `(skills, policy,
/// canonical_events)` — the exact `KernelEvent` sequence an uninterrupted run of this fixture
/// produces, the oracle every assertion below checks the crash-resumed run against.
fn fixture(
    granted_dir: &Path,
    file_path: &Path,
) -> (HashMap<String, SkillConfig>, PolicyFile, Vec<KernelEvent>) {
    let capability = Capability::FsRead(granted_dir.to_path_buf());
    let manifest = SkillManifest {
        name: "read_file".to_string(),
        requested: vec![capability.clone()],
    };
    let module_bytes = build_read_file_wasm();
    let mut skills = HashMap::new();
    skills.insert(
        "read_file".to_string(),
        SkillConfig {
            manifest,
            module_bytes,
            tainted_output: true,
        },
    );
    let mut policy_skills = HashMap::new();
    policy_skills.insert(
        "read_file".to_string(),
        HashMap::from([(capability, Decision::Grant)]),
    );
    let policy = PolicyFile {
        skills: policy_skills,
    };

    let canonical_events = vec![
        KernelEvent::UserCommand {
            text: "summarize my notes.txt".to_string(),
            tainted: false,
        },
        KernelEvent::LlmResponse {
            text: "let me check that".to_string(),
            tool_call: Some(ToolCall {
                name: "read_file".to_string(),
                arguments: serde_json::json!({"path": file_path.to_string_lossy()}),
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
        KernelEvent::TurnComplete,
    ];
    assert_eq!(canonical_events.len(), CANONICAL_LEN);

    (skills, policy, canonical_events)
}

fn tool_call_response(event: &KernelEvent) -> ScriptedResponse {
    match event {
        KernelEvent::LlmResponse {
            text,
            tool_call: Some(tc),
            ..
        } => ScriptedResponse::chunks(vec![
            ResponseChunk::Text(text.clone()),
            ResponseChunk::ToolCall(ProviderToolCall {
                id: "call_1".to_string(),
                name: tc.name.clone(),
                arguments: tc.arguments.clone(),
            }),
        ]),
        other => panic!("expected an LlmResponse carrying a tool call, got {other:?}"),
    }
}

fn final_text_response(event: &KernelEvent) -> ScriptedResponse {
    match event {
        KernelEvent::LlmResponse {
            text,
            tool_call: None,
            ..
        } => ScriptedResponse::text(text.clone()),
        other => panic!("expected a text-only LlmResponse, got {other:?}"),
    }
}

/// `Kernel<P>` owns its provider, so inspecting a `MockProvider`'s call log *after* the kernel
/// has used it needs a provider that only borrows the real one — the same pattern
/// `pythia-kernel::tests::turn_loop` already established for exactly this need. Local to this
/// test file: no change to `pythia-provider`'s own (already-merged) public surface.
struct BorrowedProvider<'a>(&'a MockProvider);

#[async_trait]
impl<'a> Provider for BorrowedProvider<'a> {
    async fn request(
        &self,
        messages: &[Message],
        tools: &[ToolSchema],
    ) -> Result<Vec<ResponseChunk>, ProviderError> {
        self.0.request(messages, tools).await
    }
}

/// Wraps a real `MockProvider` and hangs forever (never resolves) on `hang_at_call` (1-based) —
/// this is how "kill the kernel mid-turn" is simulated without a permanent production hook (see
/// this file's module doc). Local to this test file only.
struct HangOnCall {
    inner: Arc<MockProvider>,
    hang_at_call: usize,
    calls_seen: AtomicUsize,
}

#[async_trait]
impl Provider for HangOnCall {
    async fn request(
        &self,
        messages: &[Message],
        tools: &[ToolSchema],
    ) -> Result<Vec<ResponseChunk>, ProviderError> {
        let call_number = self.calls_seen.fetch_add(1, Ordering::SeqCst) + 1;
        if call_number == self.hang_at_call {
            // Never resolves. The spawned task parks here forever until the test aborts it —
            // this is the exact "kill mid-turn" moment, deliberately placed after the prior
            // loop iteration's `ToolResult` append already returned (drive_turn's loop is
            // synchronous between `.await` points, so nothing can interleave here).
            std::future::pending::<()>().await;
            unreachable!("a hung future is never polled to completion");
        }
        self.inner.request(messages, tools).await
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn Demo_Durability_KillAfterReadFileEffect_RestartReplaysZeroReExecution_TurnCompletes() {
    // ---- Baseline: an uninterrupted run of the exact same fixture — the oracle assertion (a)
    // (provider call-count parity) and (d) (final events shape) check the crash-resumed run
    // against.
    let baseline_dir = tempfile::tempdir().expect("tempdir creates");
    let baseline_file = baseline_dir.path().join("notes.txt");
    std::fs::write(&baseline_file, b"buy milk").expect("fixture file writes");
    let (baseline_skills, baseline_policy, baseline_canonical) =
        fixture(baseline_dir.path(), &baseline_file);
    let baseline_provider = MockProvider::new(vec![
        tool_call_response(&baseline_canonical[1]),
        final_text_response(&baseline_canonical[3]),
    ]);
    let baseline_eventlog = EventLog::open_in_memory().expect("in-memory event log opens");
    let mut baseline_kernel = Kernel::new(
        baseline_eventlog,
        BorrowedProvider(&baseline_provider),
        baseline_policy,
        baseline_skills,
    );

    let baseline_outcomes = execute(
        &mut baseline_kernel,
        CliCommand::Run {
            text: "summarize my notes.txt".to_string(),
        },
    )
    .await
    .expect("baseline (never-crashed) run must not error");

    assert_eq!(
        baseline_outcomes.len(),
        1,
        "no open turn to resume: exactly the one new turn's outcome"
    );
    assert_eq!(
        baseline_outcomes[0].events, baseline_canonical,
        "sanity: the baseline fixture must produce the canonical E1..E5 shape"
    );
    let baseline_call_count = baseline_provider.call_count();
    assert_eq!(
        baseline_call_count, 2,
        "sanity: this fixture always makes exactly 2 provider calls in an uninterrupted run"
    );

    // ---- Crash half: a real `Kernel`, real wasmtime dispatch of the real `read-file` skill,
    // killed right after `E3: ToolResult{read_file}` commits and before the turn's second
    // provider call can return.
    let crash_dir = tempfile::tempdir().expect("tempdir creates");
    let db_path = crash_dir.path().join("pythia.db");
    let notes_path = crash_dir.path().join("notes.txt");
    std::fs::write(&notes_path, b"buy milk").expect("fixture file writes");
    let (skills, policy, canonical) = fixture(crash_dir.path(), &notes_path);

    let pre_provider = Arc::new(MockProvider::new(vec![tool_call_response(&canonical[1])]));
    let hang_provider = HangOnCall {
        inner: Arc::clone(&pre_provider),
        hang_at_call: 2,
        calls_seen: AtomicUsize::new(0),
    };
    let pre_eventlog = EventLog::open(&db_path).expect("event log opens at a real file path");
    let pre_kernel = Kernel::new(pre_eventlog, hang_provider, policy.clone(), skills.clone());

    let handle = tokio::spawn(async move {
        let mut kernel = pre_kernel;
        let _ = execute(
            &mut kernel,
            CliCommand::Run {
                text: "summarize my notes.txt".to_string(),
            },
        )
        .await;
    });

    // Real wall-clock margin for the spawned task's worker thread to run the fixture's fully
    // synchronous work (history read, first provider call, real wasmtime instantiate+dispatch of
    // read-file, ToolResult append, second history read) up to the point it blocks on the hung
    // second provider call — comfortably generous for an in-process operation with no network
    // I/O, and this blocking `std::thread::sleep` runs on the *test's* worker thread, not the
    // spawned task's, so it does not itself stall the spawned task's progress.
    std::thread::sleep(Duration::from_millis(300));

    handle.abort();
    let join_result = handle.await;
    assert!(
        join_result.is_err() && join_result.unwrap_err().is_cancelled(),
        "expected the crash-simulated task to still be parked on the post-ToolResult provider \
         call when aborted -- if it had already finished, the hang point never actually gated \
         anything and this test would not be proving what it claims to"
    );

    // "Kill" is now complete: the aborted task owned the only live `Kernel`/`EventLog` handle,
    // so dropping its future (during abort) dropped the SQLite connection uncleanly — no
    // `close_turn` call ever ran, matching a real process crash mid-turn.

    // Precondition sanity check (not one of the four required assertions): confirm the crash
    // really did land after E3 committed, via a fresh read-only pass over the same file.
    {
        let sanity_log = EventLog::open(&db_path).expect("event log reopens post-crash");
        let open_turn_id = sanity_log
            .find_open_turn()
            .expect("find_open_turn must not error")
            .expect("the turn must still be open post-crash (never closed)");
        let rows = sanity_log
            .read_turn(&open_turn_id)
            .expect("read_turn must not error");
        assert_eq!(
            rows.len(),
            3,
            "expected exactly E1 UserCommand, E2 LlmResponse{{read_file}}, E3 \
             ToolResult{{read_file}} to have committed before the simulated crash, got {rows:?}"
        );
        assert_eq!(rows.last().unwrap().event_type, "ToolResult");
    }

    // The call-count spy (assertion (b)): delete the source file *after* confirming E3 committed.
    // A resume that incorrectly re-dispatched `read_file` would now get a filesystem error
    // instead of silently reproducing "buy milk" — the final-events assertions below would catch
    // it either way (a second `ToolResult` row, or a changed status/output on a re-executed one).
    std::fs::remove_file(&notes_path)
        .expect("removing the fixture file to prove no re-read on resume");

    // ---- Restart: a brand-new `Kernel`/`EventLog` over the *same* SQLite file path, per the
    // plan's file-level approach ("construct a fresh Kernel/EventLog against the same SQLite
    // file path and call resume()").
    let post_provider = MockProvider::new(vec![final_text_response(&canonical[3])]);
    let post_eventlog = EventLog::open(&db_path).expect("event log reopens post-crash");
    let mut post_kernel = Kernel::new(
        post_eventlog,
        BorrowedProvider(&post_provider),
        policy,
        skills,
    );

    let resumed = post_kernel
        .resume()
        .await
        .expect("resume must not error")
        .expect("resume must find the crash-abandoned open turn");

    // (c) the turn reaches TurnComplete.
    assert_eq!(resumed.status, EventLogTurnOutcome::Complete);
    assert_eq!(resumed.events.last(), Some(&KernelEvent::TurnComplete));

    // (d) the final `events` table matches the expected E1..E6 *shape* from spec §5 — this
    // demo's own fixture (Task 17's file-level approach: `read_file`, then a single turn-ending
    // text completion, no `send_email`) is the single-tool-call analog of spec §5's two-tool
    // worked example, exactly as `pythia-kernel::tests::turn_loop`'s fixture documents:
    // UserCommand, LlmResponse{tool_call: read_file}, ToolResult{read_file}, LlmResponse{no
    // tool_call}, TurnComplete.
    assert_eq!(resumed.events, canonical);

    // (b) the read-file effect was recorded exactly once across the whole crash-resume run, with
    // its original (pre-deletion) content — proof the committed ToolResult was replayed as a
    // fact, never re-executed.
    let read_file_results: Vec<&KernelEvent> = resumed
        .events
        .iter()
        .filter(
            |event| matches!(event, KernelEvent::ToolResult { tool, .. } if tool == "read_file"),
        )
        .collect();
    assert_eq!(
        read_file_results.len(),
        1,
        "expected exactly one ToolResult{{read_file}} across the whole crash-resume run, got \
         {}: {read_file_results:?}",
        read_file_results.len()
    );
    assert_eq!(
        read_file_results[0],
        &KernelEvent::ToolResult {
            tool: "read_file".to_string(),
            status: "ok".to_string(),
            output: "buy milk".to_string(),
            reason: None,
            tainted: true,
        },
        "the single recorded ToolResult must carry the original file content -- a second, \
         re-executed read would have failed against the now-deleted file instead"
    );

    // (a) total provider call count across pre-/post-crash halves equals the non-crashed
    // baseline's — no extra call was made for anything replay already had a recorded fact for.
    assert_eq!(
        pre_provider.call_count(),
        1,
        "expected exactly one pre-crash provider call (the read_file tool-call request) — the \
         second call was gated to hang and therefore never delegated to the real MockProvider"
    );
    assert_eq!(
        post_provider.call_count(),
        1,
        "expected exactly one post-crash provider call (the final text completion) — resume \
         must not re-issue the already-answered first call"
    );
    let total_calls = pre_provider.call_count() + post_provider.call_count();
    assert_eq!(
        total_calls, baseline_call_count,
        "total provider calls across the crash-resume run ({total_calls}) must equal the \
         non-crashed baseline's ({baseline_call_count}) — any excess would mean replay re-issued \
         a call it already had a recorded fact for"
    );
}
