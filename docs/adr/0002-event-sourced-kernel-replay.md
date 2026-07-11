# 0002 — Event-sourced kernel with replay-only-unexecuted-effects rule

**Status:** Accepted
**Date:** 2026-07-10
**Related:** `docs/superpowers/specs/2026-07-10-pythia-engine-design.md` §4 (Unit 1, Unit 2), §5, §7;
`docs/reference/hermes-systems-architecture.md` §4.3, §11

## Context

Spec weakness 2 (durability): Hermes' agent loop is in-memory. Hermes does incrementally flush the
transcript to `SessionDB` between tool calls specifically so a "destructive-but-valid" tool call
cannot lose the conversation — a real and validated mitigation — but the *turn loop's own control
state* (retry state, iteration budget, which tool call is in flight) is not itself replayed. A crash
between "the LLM decided to call a tool" and "the tool finished" loses that in-flight step and there
is no replay or time-travel debug (spec §1, weakness 2; confirmed absent in the reference source).

Pythia's system outcome (spec §3) is explicit: "a turn survives a mid-turn crash: on restart the
kernel replays the event log, re-executes nothing that already has a recorded result, and continues
from the log tip." This must hold even though the driving LLM is non-deterministic and several tools
(email send, file write) are non-idempotent side effects — replaying "the outside world" is not an
option, only replaying the *decision* to have already produced a *recorded fact* is.

## Decision

The kernel treats the event log as the single source of truth for turn state, not a convenience log
beside a live in-memory loop:

- Every step of a turn — user command, LLM response, tool result, turn completion — is appended to
  the event log as a typed event (kernel-side vocabulary: `UserCommand`, `LlmResponse`, `ToolResult`,
  `TurnComplete`), translated to/from the log's generic envelope by the kernel (ADR-0001).
- **Replay rule:** an event with a recorded `effect_result` is a *fact* and is never re-executed.
  Only events past the log tip execute.
- On startup (fresh or post-crash) the kernel reads the log from the beginning (or a checkpoint),
  reconstructs turn state and provider-call context purely from recorded events, and resumes
  execution strictly after the last event that has an effect result.
- The kernel holds no state that is not reconstructible from the log — no hidden mutable struct is
  the source of truth for "what happened."

Concretely (spec §5): if the kernel dies after `E3: ToolResult{read_file}` is journaled, restart
reads `E1..E3`, sees `E3` already has a result, does **not** re-read the file, feeds `E1..E3` to the
provider, and continues from the next undetermined step. The side effect (`read_file`) is never
double-executed.

## Consequences

**+**
- Crash-mid-turn is provably safe and is directly demonstrable as the slice's durability demo: kill
  the process after a recorded effect, restart, assert zero re-execution of that effect and identical
  continuation.
- Non-idempotent effects (email send, file write) cannot be double-executed across a crash boundary,
  closing exactly the gap Hermes' incremental transcript-flush does not close (it protects the
  *transcript*, not the *loop's own resumption correctness*).
- The log is simultaneously the durability mechanism and a time-travel debug / audit trail —
  extending the SQL-surface reuse Hermes validated for its own `state.db`, but for structured,
  typed turn events rather than only session transcript text.
- Kernel logic must be written so all state is reconstructible from the log; this is a discipline
  constraint, but it is a good one — it forces the kernel to stay a pure function of `(log, new
  input) -> next event`, which is also what makes it independently testable per spec §7.

**−**
- Every step pays a synchronous durable-write cost (a SQLite WAL commit, ADR-0004) before the kernel
  proceeds to the next step — added per-step latency versus a pure in-memory loop. Acceptable given
  the local-model-first cost profile (network/inference latency to Ollama already dominates).
- Replay correctness depends on `effect_result` being recorded atomically with, or strictly after,
  the side effect it describes. A bug where the effect executes but the result write fails (or is
  reordered ahead of the effect) would violate the at-most-once guarantee this ADR promises. The
  write path for `ToolResult` events must be built with this ordering as an explicit invariant, and
  ideally tools are additionally idempotent or dedup-key-tracked as defense in depth (not required
  for the slice, worth revisiting if/when non-idempotent tools multiply).
- The kernel cannot "fix" a bad decision by mutating history — the log is append-only by design
  (ADR-0004), so correcting a bad turn requires a new corrective event, not an edit. This is the
  correct tradeoff for an audit trail but is a real constraint on how error-recovery logic is
  written.
