# Pythia — First Vertical Slice: Delivery Tracking

**Source plan:** `docs/superpowers/plans/pythia-vertical-slice.md` (18 tasks, 8 waves, lead-engineer
implementation plan — this doc transcribes and tracks it; it does not re-decide granularity or
sequencing).
**Tracker:** GitHub repo `bRRRITSCOLD/pythia`.
**Milestone:** [Vertical slice — thin thread](https://github.com/bRRRITSCOLD/pythia/milestone/1)
(all 18 issues attached).
**Labels:** `wave-0`..`wave-7`, `critical-path`, `parallel-safe`, `p0-security`.
**Last synced:** 2026-07-10 (all issues just created — 0 of 18 done).

---

## Done criteria for the slice

Both demos green as integration tests (spec §6, plan's two demo tasks):

1. **Durability demo** (`Task 17` / issue #17, `crates/cli/tests/demo_durability.rs`) — kill the
   kernel mid-turn after a `read_file` effect commits → restart → replays without re-executing the
   effect → turn completes.
2. **Safety demo** (`Task 18` / issue #19, `crates/cli/tests/demo_safety.rs`) — an injected exfil
   instruction targets a skill with no `net` grant → SR-2's four assertions hold (import absent,
   dispatch-time failure, zero socket syscalls, denial logged).

The slice is **done** when issues #17 and #19 are both closed (tests green in CI) and every P0
security requirement (SR-1..6, table below) has its owning task closed.

---

## P0 security requirement -> owning issue (no gaps)

| Requirement | Owning issue | Confirming issue |
|---|---|---|
| SR-1 Fail-closed capability default | #2 (Task 2) | #19 (Task 18) |
| SR-2 Rigorous 4-assertion safety demo | #19 (Task 18) | mechanism built in #4/#5/#15 (Tasks 4, 5, 15) — see note below |
| SR-3 Per-call scope re-check | #6 (Task 6) | — |
| SR-4 Zero WASI ambient authority | #5 (Task 5) | — |
| SR-5 Secrets never persisted/replayed plaintext | #8 (Task 8) | #16 (Task 16) |
| SR-6 Fuel + memory limits | #7 (Task 7) | — |

Note: the plan's own §1 table lists SR-2's "mechanism built in" as Tasks 5, 9, 15 — issue numbers
#5, #9, #15 respectively.

---

## Wave table: plan task -> issue -> status

| Wave | Task | Title | Issue | Blocked by (issues) | Parallel-safe | Critical path | Owns SR | Status |
|---|---|---|---|---|---|---|---|---|
| 0 | 1 | Workspace scaffold | [#1](https://github.com/bRRRITSCOLD/pythia/issues/1) | none | no | yes | — | open |
| 1 | 2 | `pythia-manifest`: capability vocabulary, manifest/policy schema, fail-closed resolution | [#2](https://github.com/bRRRITSCOLD/pythia/issues/2) | #1 | yes (w/ 3, 4) | yes | SR-1 | open |
| 1 | 3 | `pythia-eventlog`: SQLite/WAL envelope store, replay-cursor reads | [#3](https://github.com/bRRRITSCOLD/pythia/issues/3) | #1 | yes (w/ 2, 4) | yes | — | open |
| 1 | 4 | `pythia-provider`: trait, wire-agnostic types, contract test suite, `MockProvider` | [#4](https://github.com/bRRRITSCOLD/pythia/issues/4) | #1 | yes (w/ 2, 3) | yes | — | open |
| 2 | 5 | `pythia-capability-host`: wasmtime mechanism, Linker-from-grants, zero-ambient WASI | [#5](https://github.com/bRRRITSCOLD/pythia/issues/5) | #2 | no (blocks 6,7,8) | yes | SR-4 | open |
| 2 | 10 | `pythia-provider-ollama`: OpenAI-compatible client against Ollama/qwen3.5 (start) | [#10](https://github.com/bRRRITSCOLD/pythia/issues/10) | #4 | yes | no | — | open |
| 2 | 11 | `pythia-skill-sdk`: skill-side bindings | [#11](https://github.com/bRRRITSCOLD/pythia/issues/11) | #1, #2 | yes (w/ 5, 10) | no | — | open |
| 2 | 14 | `pythia-kernel`: typed event vocabulary + envelope translation | [#14](https://github.com/bRRRITSCOLD/pythia/issues/14) | #3 | yes (w/ 5, 10, 11) | no | — | open |
| 3 | 6 | `pythia-capability-host`: `fs_read` host function, per-call scope re-check | [#6](https://github.com/bRRRITSCOLD/pythia/issues/6) | #5 | yes (w/ 7, 8) | yes | SR-3 | open |
| 3 | 7 | `pythia-capability-host`: fuel + memory limits | [#7](https://github.com/bRRRITSCOLD/pythia/issues/7) | #5 | yes (w/ 6, 8) | yes | SR-6 | open |
| 3 | 8 | `pythia-capability-host`: `secret_get` host function + mandatory result redaction | [#8](https://github.com/bRRRITSCOLD/pythia/issues/8) | #5 | yes (w/ 6, 7) | yes | SR-5 | open |
| 3 | 12 | `skills/read-file`: durability-demo skill | [#12](https://github.com/bRRRITSCOLD/pythia/issues/12) | #11 | yes (w/ 13) | no | — | open |
| 3 | 13 | `skills/send-email`: safety-demo skill | [#13](https://github.com/bRRRITSCOLD/pythia/issues/13) | #11 | yes (w/ 12) | no | — | open |
| 3 | 10 | (`pythia-provider-ollama`, finish — see wave 2) | [#10](https://github.com/bRRRITSCOLD/pythia/issues/10) | #4 | yes | no | — | open |
| 4 | 9 | `pythia-capability-host`: `execute()` — the crate's public boundary | [#9](https://github.com/bRRRITSCOLD/pythia/issues/9) | #6, #7, #8 | no | yes | — | open |
| 5 | 15 | `pythia-kernel`: turn-loop state machine, replay, dispatch | [#15](https://github.com/bRRRITSCOLD/pythia/issues/15) | #14, #4, #9 | partial (see issue) | yes | — | open |
| 6 | 16 | `pythia-cli`: composition root, command parsing, stdout rendering | [#16](https://github.com/bRRRITSCOLD/pythia/issues/16) | #3, #9, #10, #15 | no | yes | — | open |
| 7 | 17 | Integration test: **durability demo** | [#17](https://github.com/bRRRITSCOLD/pythia/issues/17) | #9, #12, #16 | can run w/ #19 | yes | — | open |
| 7 | 18 | Integration test: **safety demo (SR-2)** | [#19](https://github.com/bRRRITSCOLD/pythia/issues/19) | #9, #13, #16 | can run w/ #17 | yes | SR-2 | open |

**Numbering note:** issue #18 does not exist — a duplicate "Task 1" issue was created during a
transient `gh` CLI hiccup and deleted; Task 18 landed as issue #19. No dependency edges reference
issue #18, so this is cosmetic only.

---

## Critical path (longest pole, 8 waves deep)

```
#1 -> #2 -> #5 -> {#6, #7, #8} -> #9 -> #15 -> #16 -> {#17, #19}
```

The capability-host wave (issues #5, #6, #7, #8, #9) is the structural bottleneck — every P0
security requirement funnels through it.

## Parallel-safe lanes (independent once their wave opens)

- **Provider lane:** #4 -> #10 (fully independent of capability-host and skills lanes)
- **Skills lane:** #11 -> {#12, #13} (only needs #2's manifest schema and #5's mechanism for a real
  `execute()` call at integration-test time, not to author the skill code itself)
- **Kernel-prep lane:** #14 (independent of everything except #3)

---

## Status snapshot

All 18 issues open, 0 closed, 0 blocked (nothing has started — every issue is either wave-0 or
waiting on a same-repo dependency that is itself still open). Wave 0 (#1) is the only
currently-dispatchable issue; waves 1+ open as their `Blocked by` predecessors close.

This table is the durable ledger for the slice — update the Status column (`open` / `in-progress` /
`blocked` / `closed`) as issues move, rather than re-deriving state from scratch each time.
