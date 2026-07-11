# 0004 — SQLite (WAL mode) via rusqlite as the event log store

**Status:** Accepted
**Date:** 2026-07-10
**Related:** `docs/superpowers/specs/2026-07-10-pythia-engine-design.md` §4 (Unit 2);
`docs/reference/hermes-data-architecture.md` (lines ~19–26, ~90–93, ~685, ~710–712)

## Context

ADR-0002 requires an atomic, crash-safe, queryable append-only store for the event log, with zero
external operational surface (a single binary + a single file — Pythia must be affordable to leave
running unattended, per the cost NFR). The log also doubles later as the source for cost rollups and
time-travel debugging (spec §4, Unit 2), so a bare flat-file append log with no query surface would
under-serve that stated reuse.

Hermes already validated this exact choice for its own session store: the reference data-architecture
review confirms "SQLite in WAL mode = concurrent readers + a single writer" as the correct call for a
"local-first, single-node system with deliberately no [external services]," and separately flags the
one operational trap that matters: `state.db` must be backed up WAL-aware (`sqlite3 .backup` /
`VACUUM INTO`, or a checkpoint before copying) — "a naive file copy mid-write can capture a torn DB +
orphaned WAL."

## Decision

Use **SQLite in WAL mode**, accessed via **rusqlite** (synchronous, safe Rust FFI bindings), as the
sole store for the event log. The spec's envelope shape —

```
seq | turn_id | type | payload (json) | effect_result (json, nullable) | tainted | created_at
```

— is the logical unit `pythia-eventlog` exposes to the kernel (`append`, `read_from(seq)`); it has no
knowledge of the kernel's typed event vocabulary (`UserCommand`, `LlmResponse`, …), which is the
kernel's own translation (ADR-0001), keeping the store generic and reusable. The concrete DDL
(a `turns` aggregate-root table plus an immutable, trigger-enforced `events` child table, rather than
a single flat table) is specified in `docs/superpowers/data/pythia-data-model.md` — that refinement
is a schema-level detail within this ADR's scope, not a departure from it: it remains one SQLite file,
WAL mode, one writer, append-only, with the same replay contract.

## Consequences

**+**
- Single file, atomic per-row commit — append is exactly the durability unit ADR-0002's replay rule
  needs (an event either has a durable `effect_result` or it does not; there is no partial state).
- Zero operational surface: no server process, no network port, no separate ops runbook — directly
  serves the "leave running unattended, affordably" thesis.
- Full SQL surface is available for the stated future reuse (cost rollups, time-travel debug queries)
  without introducing a second store.
- A proven pattern: Hermes' own reference architecture independently reached and validated the same
  choice for the same class of problem (local-first, single-node, durable structured log).

**−**
- WAL mode is single-writer: concurrent appends serialize. Fine for a single-turn-at-a-time kernel
  loop in this slice; a future concurrent-tool-dispatch or multi-turn-parallelism feature would need
  an explicit write-serialization design (e.g., a dedicated writer thread or queue) rather than
  assuming free concurrent writes.
- rusqlite is synchronous. If the kernel or provider layer runs on an async runtime (see the tokio
  selection in the architecture summary), event-log calls must be dispatched off the async reactor
  (e.g., `spawn_blocking` or a dedicated blocking thread) — a small integration seam that must be
  handled deliberately, not assumed away.
- Backup/replication requires WAL-aware tooling (`sqlite3 .backup` / `VACUUM INTO`, or a checkpoint
  before any file copy) — a naive copy can capture a torn database. Out of scope for the slice, but
  must be documented before any operational (non-development) use.
