# Handoff — Pythia design phase complete, ready for build

## 1. Goal

Recover the lost Pythia engine brainstorm (original handoff was wiped from `/tmp`), then run the
design phases of `/compainy:deliver` — spec → architecture → data → threat model → implementation
plan → tracked issues — **stopping before build**. Build happens in the next session.

## 2. Done

- **Recovered the original brainstorm** from the `ai`-repo transcript (session `cb0e389d`,
  2026-07-09): thesis, Rust+wasmtime bet, four-unit architecture, event-log replay rule,
  BYO-key constraint, cost-tiering ideas, 7 differentiation vectors vs Hermes.
- **Phase 0 spec** — `docs/superpowers/specs/2026-07-10-pythia-engine-design.md`. Approved by user.
  Open questions closed via Q&A: thin-thread slice (all 4 units), hand-written Rust→WASM skills,
  SQLite/WAL event log, Provider trait w/ Ollama impl first, manifest(request)+policy(authority).
- **Phase 1 architecture** (systems-architect agent) — `docs/superpowers/architecture/pythia-architecture.md`
  + ADRs `docs/adr/0001..0006`. Two Cargo workspaces (root `crates/` + `skills/` wasm32-wasip1);
  7 crates; acyclic deps, kernel = sole orchestrator.
- **Phase 1 threat model** (security-architect agent) — `docs/superpowers/security/pythia-threat-model.md`.
  STRIDE by boundary, 11-row injection-escape enumeration, SR-1..17 (6 P0s).
- **Phase 2 data model** (data-architect agent) — `docs/superpowers/data/pythia-data-model.md`.
  Final DDL: `turns` + `events`, replay rule as CHECK, immutability triggers, per-event tx boundary,
  partial indexes.
- **Phase 3 implementation plan** (lead-engineer agent) — `docs/superpowers/plans/pythia-vertical-slice.md`.
  18 PR-sized tasks, 8 waves, critical path 1→2→5→{6,7,8}→9→15→16→{17,18}; every P0 SR owned by a task;
  Ollama-live tests `#[ignore]`-gated, merge gates on MockProvider.
- **Phase 4 tracking** (project-manager agent) — 18 GitHub issues in `bRRRITSCOLD/pythia`
  (#1–#17, #19; #18 retired dup), labels (wave-0..7, critical-path, parallel-safe, p0-security),
  milestone "Vertical slice — thin thread", tracking doc
  `docs/superpowers/plans/pythia-vertical-slice-tracking.md`.
- **Repo wired**: git init, pushed to https://github.com/bRRRITSCOLD/pythia (main, 4 commits).

## 3. In progress

Nothing dangling. Design phase is complete and committed.

## 4. Next steps (next session)

1. **Run `/compainy:orchestrate`** against the milestone "Vertical slice — thin thread" in
   `bRRRITSCOLD/pythia` — user explicitly chose orchestrate for the build phase.
2. Only issue #1 (workspace scaffold) is dispatchable at start; waves unlock per blocked-by links.
3. Before dispatching specialists, consider `.ai/stack-profile.md` (stack = pure Rust + wasmtime +
   rusqlite + tokio + reqwest; NOT the plugin default TanStack/Go stack). No frontend/UX lanes.
4. Ollama qwen3.5 is installed locally — live-model integration tests use it; merge gates don't.
5. Done criteria for the slice: issues #17 (durability demo) and #19 (safety demo, SR-2 four
   assertions) closed with green integration tests.

## 5. Key files

All in `/home/blaine-richardson/Code/github/bRRRITSCOLD/pythia` (pushed to GitHub):

- `docs/superpowers/specs/2026-07-10-pythia-engine-design.md` — the design contract
- `docs/superpowers/architecture/pythia-architecture.md` — crates, C4, NFRs, tradeoffs
- `docs/adr/0001-four-unit-orthogonal-architecture.md` … `0006-wasm32-wasip1-target.md`
- `docs/superpowers/data/pythia-data-model.md` — final DDL
- `docs/superpowers/security/pythia-threat-model.md` — SR-1..17
- `docs/superpowers/plans/pythia-vertical-slice.md` — THE build plan (read first in build session)
- `docs/superpowers/plans/pythia-vertical-slice-tracking.md` — task→issue map, wave table
- `docs/reference/hermes-{systems,data,security}-architecture.md` — weakness map (background)

## 6. Decisions

- **Thesis**: safe + durable + cheap — "the agent engine you can afford to leave running."
  Rust+wasmtime collapses safe+durable into one mechanism (no ambient authority + determinism).
- **Slice scope**: thin thread through all 4 units; proves durability (crash-resume, zero
  re-execution) and safety (no-net skill cannot exfil — import absent) end-to-end.
- **Skills**: hand-written Rust→wasm32-wasip1 for the slice; self-authoring deferred.
- **Event log**: SQLite/WAL; replay rule enforced as DB CHECK + immutability triggers; per-event
  transactions (NOT per-turn — that would break crash-resume).
- **Provider**: trait first, Ollama OpenAI-compat impl; BYO-key only, never subscription auth.
- **Capabilities**: manifest = request, policy = authority, fail-closed default (SR-1);
  per-call scope re-check (SR-3); zero WASI ambient authority (SR-4).
- **Security framing correction** (threat model): wasmtime buys ambient-authority prevention,
  NOT argument-level safety — confused-deputy/argument attacks are the policy layer's job.
- **No EventStore/SkillExecutor traits** — single implementers, YAGNI (recorded in plan §0).
- **Handoffs**: `/tmp` handoffs get wiped (lost the original one this way) — durable copy now
  committed in-repo under `docs/superpowers/handoffs/`.

## 7. Open questions

- Policy file `prompt` grant mode: blocks the CLI loop for human input — exact UX deferred (P1, SR-10).
- Compacted-context algorithm (cost lever): slice uses full history; design when router lands.
- WASI preview2/component-model migration: ADR-0006 chose preview1 for the slice; revisit when
  skill ecosystem grows.
- Issue #18 is a retired duplicate on GitHub — cosmetic gap in numbering, ignore.

## 8. Next-session prompt (copy-paste)

```
read docs/superpowers/handoffs/2026-07-10-design-phase-complete.md to catch up, then run
/compainy:orchestrate for the "Vertical slice — thin thread" milestone in bRRRITSCOLD/pythia.
Use the Workflow tool (dynamic multi-agent orchestration) for the dispatch loop: fan out
parallel-safe issues within each wave as concurrent agents, respect blocked-by links,
staff-engineer review gate on every diff before merge, squash-merge off main. Stack is pure
Rust (wasmtime, rusqlite, tokio, reqwest) — write .ai/stack-profile.md first so specialists
don't assume the default stack. Merge gates run on MockProvider; Ollama qwen3.5 live tests
are #[ignore]-gated. Done = issues #17 (durability demo) and #19 (safety demo) closed with
green integration tests.
```
