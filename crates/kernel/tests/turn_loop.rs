//! Integration tests for `pythia-kernel`'s turn-loop state machine (Task 15): `Kernel::run_turn`
//! and `Kernel::resume`, exercised end-to-end against the real `read-file` skill (Task 12) via
//! `pythia_capability_host::execute()` (Task 9) and a scripted `MockProvider` (Task 4) — no live
//! Ollama, matching the plan's merge-gate table (§4: `pythia-kernel` tests use `MockProvider`,
//! never a live model).
//!
//! All three tests share one fixture: a single-`read_file`-dispatch turn shaped like spec §5's
//! worked example (open → call provider → dispatch tool → tool result → call provider again →
//! complete). Under data model doc §5's `next_action` rule (implemented verbatim in `turn.rs`,
//! "a `ToolResult` always yields another provider call, never a direct close"), this shape is
//! five events: `UserCommand`, `LlmResponse{tool_call}`, `ToolResult`, `LlmResponse{no tool_call}`,
//! `TurnComplete` — the plan's own test list labels this fixture "E1..E6" after spec §5's *two*
//! tool-call illustration; this crate's tests use the single-tool-call shape instead (matching
//! `RunTurn_ScriptedProviderThenTool_ProducesExpectedEventSequence`'s own description, "using ...
//! the real read-file skill" — singular), which is the minimal fixture that exercises every
//! `next_action` branch without a second skill.

#![allow(non_snake_case)]

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::process::Command;

use async_trait::async_trait;
use pythia_eventlog::{EventLog, EventRow, NewEvent, TurnId, TurnOutcome as EventLogTurnOutcome};
use pythia_kernel::{Kernel, KernelEvent, SkillConfig, ToolCall};
use pythia_manifest::{Capability, Decision, PolicyFile, SkillManifest};
use pythia_provider::mock::{MockProvider, ScriptedResponse};
use pythia_provider::{
    Message, Provider, ProviderError, ResponseChunk, ToolCall as ProviderToolCall, ToolSchema,
};

/// Builds the real `read-file` skill for `wasm32-wasip1` (same approach as
/// `pythia-capability-host`'s own `tests/execute.rs` and this crate's `dispatch.rs` unit tests) —
/// exercising the actual durability-demo skill, not a synthetic WAT fixture.
fn build_read_file_wasm() -> Vec<u8> {
    let manifest_dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    let repo_root = manifest_dir
        .parent()
        .expect("crates/kernel must have a parent (crates/)")
        .parent()
        .expect("crates/ must have a parent (the repo root)");
    let skills_workspace_dir = repo_root.join("skills");

    let status = Command::new(env!("CARGO"))
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

/// Number of events in the fixture's canonical, never-crashed run.
const CANONICAL_LEN: usize = 5;

/// Wires the `read_file` skill (granted over `granted_dir`) and returns `(skills, policy,
/// canonical_events)` — the exact `KernelEvent` sequence a real, uninterrupted run of this
/// fixture produces, the oracle every test below checks its outcome against.
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

/// The provider script the fixture's shape needs, in order: the tool-call response (canonical
/// index 1) and the final text-only response (canonical index 3). `skip` drops however many of
/// these a truncated history already recorded, so a resumed run only re-issues what's missing.
fn scripted_provider_from(canonical: &[KernelEvent], skip: usize) -> MockProvider {
    let mut scripts = vec![
        tool_call_response(&canonical[1]),
        final_text_response(&canonical[3]),
    ];
    scripts.drain(0..skip.min(scripts.len()));
    MockProvider::new(scripts)
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

/// `Kernel<P>` owns its provider, so a test that needs to inspect a `MockProvider`'s call log
/// *after* the kernel has finished with it needs a provider that only borrows the real one.
/// Local to this test file — no change to `pythia-provider`'s own (already-merged, already
/// reviewed) public surface.
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

/// Inserts `canonical[..n]` directly (bypassing the normal turn loop, matching how Task 18's own
/// safety-demo test seeds the log directly), leaving `turns.status = 'open'` throughout —
/// simulating a crash immediately after the `n`th event committed and before anything else. The
/// first event always goes through `open_turn` (the real atomic turn-open, data model doc §6);
/// every subsequent one goes through a single `append` call, matching the per-event transaction
/// boundary the durability guarantee actually depends on.
fn seed_truncated_turn(eventlog: &mut EventLog, canonical: &[KernelEvent], n: usize) -> TurnId {
    assert!(
        (1..=canonical.len()).contains(&n),
        "n must select a non-empty prefix of the fixture"
    );
    let opening: EventRow = canonical[0].clone().into();
    let turn_id = eventlog
        .open_turn(&opening.payload_json, opening.tainted)
        .expect("open_turn");
    for event in &canonical[1..n] {
        let row: EventRow = event.clone().into();
        eventlog
            .append(
                &turn_id,
                NewEvent {
                    event_type: &row.event_type,
                    payload_json: &row.payload_json,
                    effect_result: row.effect_result.as_deref(),
                    tainted: row.tainted,
                },
            )
            .expect("append");
    }
    turn_id
}

#[tokio::test]
async fn RunTurn_ScriptedProviderThenTool_ProducesExpectedEventSequence() {
    let dir = tempfile::tempdir().expect("tempdir creates");
    let file_path = dir.path().join("notes.txt");
    std::fs::write(&file_path, b"buy milk").expect("fixture file writes");

    let (skills, policy, canonical) = fixture(dir.path(), &file_path);
    let provider = scripted_provider_from(&canonical, 0);
    let eventlog = EventLog::open_in_memory().expect("open in-memory event log");

    let mut kernel = Kernel::new(eventlog, provider, policy, skills);
    let outcome = kernel
        .run_turn("summarize my notes.txt")
        .await
        .expect("run_turn should succeed");

    assert_eq!(outcome.status, EventLogTurnOutcome::Complete);
    assert_eq!(
        outcome.events, canonical,
        "expected the E1..E5 shape: UserCommand, LlmResponse+tool_call, ToolResult, \
         LlmResponse(final text), TurnComplete"
    );
}

#[tokio::test]
async fn Resume_NoOpenTurn_ReturnsNone() {
    let eventlog = EventLog::open_in_memory().expect("open in-memory event log");
    let provider = MockProvider::new(vec![]);
    let mut kernel = Kernel::new(eventlog, provider, PolicyFile::default(), HashMap::new());

    let outcome = kernel.resume().await.expect("resume should succeed");

    assert!(outcome.is_none());
}

#[tokio::test]
async fn Resume_TurnAlreadyClosed_ReturnsNone() {
    // A completed turn is "no open turn" too, not just "never had one" -- resume() must not
    // re-drive a turn that already reached TurnComplete for real.
    let dir = tempfile::tempdir().expect("tempdir creates");
    let file_path = dir.path().join("notes.txt");
    std::fs::write(&file_path, b"buy milk").expect("fixture file writes");
    let (skills, policy, canonical) = fixture(dir.path(), &file_path);
    let provider = scripted_provider_from(&canonical, 0);
    let eventlog = EventLog::open_in_memory().expect("open in-memory event log");

    let mut kernel = Kernel::new(eventlog, provider, policy, skills);
    kernel
        .run_turn("summarize my notes.txt")
        .await
        .expect("run_turn should succeed");

    let outcome = kernel.resume().await.expect("resume should succeed");

    assert!(outcome.is_none());
}

/// The durability guarantee's core unit test (spec §7): for every valid crash boundary in the
/// fixture's interior (`n` = 1..4 — a crash can only land *between* two per-event transactions,
/// never inside or after the atomic turn-close that inserts `TurnComplete`, data model doc §6),
/// truncate the log at `n`, call `resume()`, and assert replay re-executes nothing already
/// recorded and still reaches the identical final sequence.
///
/// `n = CANONICAL_LEN` (the fully-closed turn) is deliberately not included in this loop: a
/// `TurnComplete` row and `turns.status = 'open'` can never coexist for a real close (the same
/// atomicity `Resume_TurnAlreadyClosed_ReturnsNone` above exercises), so simulating that
/// combination here would be testing a state the kernel itself can never produce, not the
/// durability guarantee.
#[tokio::test]
async fn Replay_TruncatedAtEachBoundary_ReExecutesNothing() {
    for n in 1..CANONICAL_LEN {
        let dir = tempfile::tempdir().expect("tempdir creates");
        let file_path = dir.path().join("notes.txt");
        std::fs::write(&file_path, b"buy milk").expect("fixture file writes");

        let (skills, policy, canonical) = fixture(dir.path(), &file_path);

        let mut eventlog = EventLog::open_in_memory().expect("open in-memory event log");
        seed_truncated_turn(&mut eventlog, &canonical, n);

        // canonical[2] (E3, the ToolResult) is already recorded once n >= 3. Deleting the source
        // file makes any accidental re-dispatch impossible to mistake for success: a genuine
        // re-read would come back empty (the file is gone) rather than silently reproducing
        // "buy milk", so a bug that re-executes an already-recorded effect surfaces loudly in
        // the final-sequence assertion below, instead of passing by coincidence.
        if n > 2 {
            std::fs::remove_file(&file_path)
                .expect("removing fixture file to prove no re-read on resume");
        }

        // The two provider responses this fixture needs are canonical[1] (tool-call) and
        // canonical[3] (final text) -- skip whichever of those the truncated prefix already
        // recorded.
        let already_recorded = (n >= 2) as usize + (n >= 4) as usize;
        let provider = scripted_provider_from(&canonical, already_recorded);
        let expected_remaining_calls = 2 - already_recorded;

        let borrowed = BorrowedProvider(&provider);
        let mut kernel = Kernel::new(eventlog, borrowed, policy, skills);

        let outcome = kernel
            .resume()
            .await
            .unwrap_or_else(|e| panic!("resume() must succeed for prefix n={n}: {e}"))
            .unwrap_or_else(|| panic!("resume() must find the open turn for prefix n={n}"));

        assert_eq!(
            outcome.status,
            EventLogTurnOutcome::Complete,
            "prefix n={n}: turn must reach Complete on resume"
        );
        assert_eq!(
            outcome.events, canonical,
            "prefix n={n}: resumed sequence must be identical to an uncrashed run, regardless \
             of where the crash happened"
        );
        assert_eq!(
            provider.call_count(),
            expected_remaining_calls,
            "prefix n={n}: provider must be called exactly {expected_remaining_calls} more \
             time(s) -- never re-issuing a call whose result is already in the truncated history"
        );
    }
}
