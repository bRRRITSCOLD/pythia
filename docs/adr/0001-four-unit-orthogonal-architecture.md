# 0001 — Four-unit orthogonal architecture (kernel / event log / capability host / skill runtime + provider seam)

**Status:** Accepted
**Date:** 2026-07-10
**Related:** `docs/superpowers/specs/2026-07-10-pythia-engine-design.md` §4; `docs/reference/hermes-systems-architecture.md` §3–4

## Context

Pythia's thesis is **safe + durable + cheap by construction**. That bet only holds if the mechanisms
that provide safety and durability are isolated enough to be independently proven — a design smeared
across one large control-flow function cannot be independently tested or reasoned about.

The reference system, Hermes, illustrates the failure mode this ADR avoids: its agent core is a
single Python process whose turn loop (`agent/conversation_loop.py::run_conversation`) is described
in-source as "the roughly 3,900-line body that drives one user turn through the agent (model call,
tool dispatch, retries, fallbacks, compression, post-turn hooks, …)." Hermes *does* get real
architectural credit for splitting provider identity, wire protocol, and compute location into three
orthogonal axes (its own ADR-001) — but the turn loop itself, and the safety/durability properties
that depend on it, are not separated the same way. Durability is best-effort (incremental SessionDB
flush); safety is a heuristic layered on top of an OS-boundary-only sandbox.

Pythia's spec requires two properties to be provable in isolation (§7, testing approach):
- the event log must be testable for replay correctness without a running kernel loop;
- the capability host must be testable for import-absence without a running kernel loop.

That requirement dictates crate boundaries, not just conceptual ones.

## Decision

Split the engine into four units plus a provider seam, each a separate crate in one Cargo workspace,
each with a single owning responsibility and its own ubiquitous language:

1. **Kernel** (`pythia-kernel`) — orchestrates one turn: reads a command, calls the provider, calls
   the capability host, journals every step, replays on resume. It is the *only* orchestrator; no
   other crate calls back into it.
2. **Event log** (`pythia-eventlog`) — owns durability. A generic append-only envelope store; it does
   not know what a "turn" or an "LLM response" is, only `(seq, turn_id, type, payload, effect_result,
   tainted, created_at)`.
3. **Capability Host** (`pythia-capability-host`) — owns safety. The wasmtime embedder; manifest
   (request) + policy (authority) resolution; import linking.
4. **Skill runtime** — the wasmtime sandbox itself plus hand-written `wasm32-wasip1` skills (a
   separate Cargo workspace under `skills/`, see ADR-0006).
5. **Provider seam** (`pythia-provider` trait + `pythia-provider-ollama`) — owns model I/O, kept
   swappable by construction (see ADR-0005).

`pythia-cli` is a thin composition root and the single input surface: it parses commands and wires
concrete implementations (SQLite path, Ollama endpoint, manifest/policy paths) into the kernel. The
kernel depends on `pythia-eventlog` and `pythia-capability-host` as concrete crates (not through an
internally-defined port trait) because each has exactly one implementation in the slice — adding a
trait indirection with a single implementer is a layer that adds no logic (see the architecture
summary doc for the DIP-vs-YAGNI reasoning). `pythia-provider` *is* a trait, because multiple
providers are a real, near-term requirement (ADR-0005).

This remains a **single OS process** — not a services mesh. The only genuine sub-process trust
boundary is the wasmtime sandbox. Pythia deliberately does not adopt Hermes' gateway/connector/NAS
topology; that operational surface exists in Hermes to solve multi-platform messaging and scale-to-
zero cron, both explicitly out of scope for this slice (spec §6).

## Consequences

**+**
- Each unit is independently unit-testable exactly as the spec's testing approach requires: the
  event log can be truncated and replayed without a kernel; the capability host can be asked to
  instantiate a skill with a missing grant and assert the import is absent, without a kernel or a
  provider.
- Avoids the "3,900-line function" failure mode — no single component owns turn orchestration,
  durability, and safety simultaneously, so a change to one cannot silently violate another.
- Safety and durability can each be proven by construction and demonstrated by the two slice demos
  (durability, safety) independently of one another.
- Zero additional operational surface versus Hermes' gateway/connector/NAS/Redis topology — one
  binary, one SQLite file, one wasmtime runtime, no servers to run.

**−**
- Four-plus crates for a first vertical slice is more Cargo boilerplate than a single crate would be;
  accepted because the safety and durability guarantees specifically require the boundary to be a
  real compilation-unit boundary, not just a module boundary, so that the host and the log can be
  exercised as standalone test targets.
- The dependency graph (kernel → eventlog, kernel → capability-host, kernel → provider trait) must be
  kept acyclic by discipline; a future change that tempts a back-reference (e.g., capability host
  wanting to journal directly) should be resisted — the kernel mediates all journaling, per ADR-0002.
