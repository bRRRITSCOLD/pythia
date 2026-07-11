# Handoff — Pythia vertical slice COMPLETE

## 1. Goal (met)

Run `/compainy:orchestrate` (autonomous-delivery) against the "Vertical slice — thin thread"
milestone in `bRRRITSCOLD/pythia`: drive all 18 tasks to done via dispatch → staff review →
security audit → squash-merge. **Done criteria: issues #17 (durability demo) and #19 (safety demo,
SR-2) closed with green integration tests.** Both met.

## 2. Final state

- **All 18 tasks CLOSED** (#1–#17, #19; #18 was a retired dup). Milestone complete.
- **Main** (`83c4a99`): `cargo fmt --all --check` clean, `cargo clippy --workspace --all-targets
  -- -D warnings` clean, `cargo test --workspace` = **140 passed / 0 failed / 2 ignored**. The 2
  ignored are the live-Ollama tests (`#[ignore]`-gated by design; merge gate runs on MockProvider).
- **Skills** (`skills/` wasm workspace): 19 tests passed, builds clean to `wasm32-wasip1`.
- **Ollama** qwen3.5 installed locally (for the ignored live tests; not required by the gate).
- Repo is **main-only**, working tree clean; all run worktrees/branches removed.

## 3. What shipped (7 crates + 3 skills, ports-and-adapters, kernel = sole orchestrator)

- `pythia-manifest` (#2) — capability vocab, manifest/policy schema, fail-closed resolve (SR-1).
- `pythia-eventlog` (#3) — SQLite/WAL append-only envelope store, replay-cursor reads.
- `pythia-provider` (#4) — Provider trait, wire-agnostic types, MockProvider; `pythia-provider-ollama`
  (#10) OpenAI-compat client.
- `pythia-capability-host` (#5–#9) — wasmtime embedder; `fs_read` per-call scope re-check (SR-3),
  fuel+memory+table limits (SR-6), `secret_get` + mandatory redaction (SR-5), zero-ambient WASI
  (SR-4), `execute()` boundary assembling all of it. Registers host fns under module `"pythia"` (#32).
- `pythia-kernel` (#14, #15) — typed event vocabulary + translation; turn-loop state machine where
  `next_action` is a pure function of event history → replay/resume re-executes nothing already
  journalled (per-event tx boundaries).
- `pythia-cli` (#16) — composition root; `run()` library entry + generic `execute<P: Provider>`
  seam the demos drive in-process; SR-5 verbatim marker rendering.
- Skills `skill-sdk` (#11), `read-file` (#12), `send-email` (#13) — hand-written Rust → wasm32-wasip1.
- Demos: `#17` durability (`crates/cli/tests/demo_durability.rs`), `#19` safety
  (`crates/cli/tests/demo_safety.rs`).

## 4. Open follow-ups (all filed this run; NONE on the slice critical path; deferred by design)

- **#34 (p0)** — SR-6 wall-clock watchdog. The convergent fix for the fuel-blind *blocking-hang*
  class: fuel meters instructions, so any blocking host call escapes it. PR #31 closed the known
  instances at the config surface (table.grow cap 10k, `wasm_multi_memory(false)`, `wasm_threads(false)`,
  `poll_oneoff` stub) across 3 security-fix passes; #34 is the durable class-closing mechanism
  (epoch-interruption or per-call worker-thread join-timeout). Demo skills don't block, so the slice
  is unaffected. **Read #34 before wiring any blocking/`net:*` capability.**
- **#36 (p0)** — harden `Instance::read_memory` (alloc-before-bounds-check, same anti-pattern already
  fixed in `read_guest_path`) + sanitize `Denied`/`Wasmtime` reason strings.
- **#38** — extend `pythia_provider::Message` with structured `tool_calls`/`tool_call_id`. `build_context`
  currently renders tool calls as text (fine for MockProvider + lenient Ollama). **Required before any
  strict OpenAI-dialect provider.**
- **#39** — SR-8 (denial reason inherits triggering LlmResponse taint) + SR-9 (exactly-once for
  non-idempotent effects — the loop is at-least-once across a crash between effect and ToolResult
  commit; harmless for idempotent read-file, **matters at the `net:*_send` milestone**).
- **#41** — `Config::from_env` real coverage (current test is a tautology), `build_kernel` skills
  injection (so the live `pythia run` UX can register a skill — the demos inject via `Kernel::new`
  directly), terminal-escape sanitization when rendering tainted content (Low; before any non-local
  channel / SR-17).

## 5. Process notes for the next orchestration run

- **Two issue-metadata gotchas fixed this run** (already corrected on GitHub): issue bodies #1–#17
  were shifted by one at creation, and the linker registered host fns under `"pythia_host"` while
  skills import `"pythia"` (#32, fixed with #9).
- **Commit-before-push discipline (bit twice):** a rebase/conflict fix that passes `cargo test` in the
  worktree but is **not committed** ships the un-fixed committed state on force-push. Once broke main
  (duplicate `register_import` from the #33 rebase). After any conflict resolution: `git add` +
  confirm `git status` clean **before** the force-push, not just that it compiles.
- **Stacked-PR trap:** overnight implementers based PRs on dependency *branches* instead of `main`;
  a few classifier-blocked "merges" were logged as merged anyway. Rebuilt main by cherry-picking the
  approved squashes in dependency order. Implementer prompts now force `--base main` off latest
  `origin/main`. The autonomous-delivery script's `merge` step also needs explicit merge permission
  (the safety classifier blocked `gh pr merge` until the user granted it).
- **Gate discipline held:** every PR passed staff-engineer review; every p0-security / security-sensitive
  diff also passed a security-architect deep audit (the capability-host wave surfaced and closed
  genuine CRITICAL/HIGH resource-exhaustion holes this way — the gate earned its cost).

## 6. Key files

- Build plan: `docs/superpowers/plans/pythia-vertical-slice.md`
- Architecture + ADRs: `docs/superpowers/architecture/pythia-architecture.md`, `docs/adr/0001..0006`
- Data model (replay/resume §5, §7): `docs/superpowers/data/pythia-data-model.md`
- Threat model (SR-1..17): `docs/superpowers/security/pythia-threat-model.md`
- Stack profile: `.ai/stack-profile.md`
- Prior handoff: `docs/superpowers/handoffs/2026-07-10-design-phase-complete.md`

## 7. Suggested next session

Slice is shippable. Natural next steps, in rough priority:
1. Close **#34** (wall-clock watchdog) + **#36** — the two p0 hardening items — before the engine
   runs untrusted skills unattended (the thesis is "the engine you can afford to leave running").
2. Then **#38** + real `net:smtp_send` body, which unlocks **#39**'s exactly-once (SR-9) work.

**Decompose first — the 5 open issues are NOT dispatchable as-is.** #34/#36/#38/#39/#41 are broad,
multi-item tracking issues (each bundles 2–3 sub-tasks), not the PR-sized, single-concern units the
orchestrate loop dispatches. The autonomous-delivery scout also reads `**Blocked by:** #N` lines from
issue bodies to order waves — these follow-ups have none yet. So run `/compainy:project-management`
(or the lead-engineer → project-manager agents) to split them into PR-sized issues with blocked-by
edges before orchestrating. Given only ~2 hardening items are urgent, running them inline via the
Agent tool is also reasonable and avoids the decomposition overhead.

## 8. Next-session prompt (copy-paste)

```
Read docs/superpowers/handoffs/2026-07-11-vertical-slice-complete.md to catch up (Pythia vertical
slice is COMPLETE — 18/18 tasks merged, both demos green on main). Next: harden the engine before it
runs untrusted skills unattended. Start with the two p0 follow-ups #34 (SR-6 wall-clock watchdog —
the convergent fix for the fuel-blind blocking-hang class) and #36 (read_memory alloc-before-bounds
+ denial-reason sanitize).

These 5 open issues (#34/#36/#38/#39/#41) are broad tracking issues, NOT PR-sized — first run
/compainy:project-management to decompose them into single-concern issues with `**Blocked by:** #N`
edges, THEN drive them with /compainy:orchestrate using the Workflow tool (mode A — the dynamic
multi-agent deliver.workflow.mjs), NOT bare main-session dispatch. Fan out parallel-safe issues per
wave, staff-engineer review gate on every diff, security-architect deep audit on every p0-security /
security-sensitive diff, squash-merge off latest main.

Stack is pure Rust (wasmtime, rusqlite, tokio, reqwest) — .ai/stack-profile.md is committed.
Merge gates run on MockProvider; live-Ollama tests stay #[ignore]-gated. NON-NEGOTIABLE process
guards from last run: (1) implementer PRs MUST base on latest origin/main, never a feature branch;
(2) after ANY rebase/conflict resolution, `git add` + confirm `git status` clean BEFORE force-push —
a working-tree fix that compiles but isn't committed ships the un-fixed state and broke main once.
```
