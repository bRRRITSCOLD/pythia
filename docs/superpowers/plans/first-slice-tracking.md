# First Slice — Delivery Tracking Ledger

Execution contract for the orchestrate/dispatch loop. Maps each plan task (T1–T17) to
its GitHub issue, wave, blockers, and status. **Source of truth for scope:**
`docs/superpowers/plans/first-slice.md`. This file owns the live delivery *state*.

- **Repo:** `bRRRITSCOLD/pythia`
- **Milestone (epic):** `first-slice` — #3 — https://github.com/bRRRITSCOLD/pythia/milestone/3
- **Owner (all tasks):** `backend-engineer` (label `owner:backend-engineer`)
- **Labels per task:** its `wave-N`, `owner:backend-engineer`, `slice-1` (+ `foundations` for Wave 0)

## T# → Issue map

| T# | Issue | Wave | Title | blockedBy (issue #) | blockedBy (T#) | Status |
|----|-------|------|-------|---------------------|----------------|--------|
| T1  | #73 | wave-0 | Module init + architecture fitness tests | — | — | open |
| T2  | #74 | wave-0 | Core domain types + sentinel errors + NewID | #73 | T1 | open |
| T3  | #75 | wave-0 | Core ports — Provider, Tool, ToolRegistry, SessionRepository | #74 | T2 | open |
| T4  | #76 | wave-0 | Core AgentEvent contract | #74 | T2 | open |
| T5  | #77 | wave-0 | Config — env → validated Config | #73 | T1 | open |
| T6  | #61 | wave-1 | Core Agent turn loop | #75, #76 | T3, T4 | open |
| T7  | #62 | wave-1 | SQLite SessionRepository adapter + migrations | #75 | T3 | open |
| T8  | #63 | wave-1 | Ollama Provider adapter (streaming) | #75 | T3 | open |
| T9  | #64 | wave-1 | Tool toolkit — arg-validation + path containment + result envelope | #74 | T2 | open |
| T10 | #65 | wave-1 | In-process ToolRegistry adapter | #75 | T3 | open |
| T11 | #66 | wave-2 | read tool | #64 | T9 | open |
| T12 | #67 | wave-2 | write tool | #64 | T9 | open |
| T13 | #68 | wave-2 | edit tool | #64 | T9 | open |
| T14 | #69 | wave-2 | bash tool | #64 | T9 | open |
| T15 | #70 | wave-2 | TUI adapter (Bubble Tea) + SR-1 sanitizer | #76, #61 | T4, T6 | open |
| T16 | #71 | wave-3 | cmd/pythia composition root (DI wiring) | #77, #61, #62, #63, #65, #66, #67, #68, #69, #70 | T5, T6, T7, T8, T10, T11, T12, T13, T14, T15 | open |
| T17 | #72 | wave-3 | e2e TUI journey (teatest) | #61, #70 | T6, T15 | open |

## Wave-ordered ready-list (dispatch order)

A wave becomes ready only when every issue in the prior waves it depends on is closed.
Tasks within a wave are parallel-safe (file-disjoint) unless noted.

### Wave 0 — READY NOW (no blockers all-satisfied at start)
- **#73 (T1)** — ready immediately (no blockers). *Must land first: unblocks T2, T5.*
- **#77 (T5)** — ready as soon as #73 closes (blockedBy T1 only).
- **#74 (T2)** — ready as soon as #73 closes (blockedBy T1). Unblocks T3, T4, T9.
- **#75 (T3)** — ready as soon as #74 closes.
- **#76 (T4)** — ready as soon as #74 closes. Parallel-safe with T3, T5.

> Strictly no-blocker at t=0: **#73 (T1)** only. After #73 closes, #77 (T5) and #74 (T2) open up; after #74, #75 (T3) and #76 (T4).

### Wave 1 — after their Wave-0 blockers close
- **#64 (T9)** — after #74 (T2). *Critical: blocks all of Wave 2 tools (T11–T14).*
- **#62 (T7)**, **#63 (T8)**, **#65 (T10)** — after #75 (T3).
- **#61 (T6)** — after #75 (T3) + #76 (T4). *Critical: blocks T15, T16, T17.*
- All five (T6–T10) are parallel-safe with each other.

### Wave 2 — after their Wave-1 blockers close
- **#66 (T11)**, **#67 (T12)**, **#68 (T13)**, **#69 (T14)** — after #64 (T9). File-disjoint, genuine parallelism.
- **#70 (T15)** — after #76 (T4) + #61 (T6).
- All five parallel-safe with each other.

### Wave 3 — last
- **#72 (T17)** — after #61 (T6) + #70 (T15).
- **#71 (T16)** — after #77, #61, #62, #63, #65, #66, #67, #68, #69, #70 (T5,T6,T7,T8,T10,T11–T15). Deliberately last; conflicts with nothing. Parallel-safe with T17.

## Critical path

`T1 (#73) → T2 (#74) → T3 (#75) → T6 (#61) → T15 (#70) → T16 (#71)`
plus the tool gate `T2 (#74) → T9 (#64) → {T11–T14} → T16 (#71)`.

The longest dependency chain to the final integrable binary runs through
**#73 → #74 → #75 → #61 → #70 → #71**. T9 (#64) is the second gate: it single-handedly
blocks all four tools (T11–T14), which in turn block T16. Prioritize **#73, #74, #75, #61, #64**
to keep the critical path moving.

## Status legend
`open` · `in-progress` (label `in-progress`) · `blocked` · `done` (issue closed)

Update the Status column and re-check wave-readiness after each merge.
