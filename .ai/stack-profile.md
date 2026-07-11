# Project Stack Profile
<!-- Read by the `compainy` plugin implementation skills; overrides their defaults.
     Discipline (TDD/DDD/pragmatic-SOLID/DRY-KISS, ports-and-adapters,
     test tiers, Subject_Scenario_Expectation naming) is invariant. -->

## Languages
- Rust (edition 2021+), only language in this repo

## Frontend
- Framework:           none — Pythia is a headless agent engine (CLI entry only)

## Backend
- Language(s):         Rust
- HTTP framework:      none — no HTTP server. Outbound HTTP only via `reqwest` (Provider adapter, Ollama OpenAI-compat)
- Validation:          plain Rust types + `serde` deserialization at adapter boundaries
- Async runtime:       tokio
- Sandbox:             wasmtime, skills compiled to `wasm32-wasip1` (separate `skills/` Cargo workspace)
# Architecture is invariant: ports-and-adapters / DI (not a stack parameter)

## Data
- Primary store(s):    SQLite via `rusqlite`, WAL mode — append-only event log (`turns` + `events`), DDL in docs/superpowers/data/pythia-data-model.md
- Cache:               none
- Search / vector:     none

## Infra
- Target:              local-only for the vertical slice
- IaC:                 none
- Local infra:         none — SQLite is embedded; Ollama (qwen3.5) installed locally for live-model tests

## Testing
- Runner(s):           cargo test (unit + integration)
- E2E:                 integration tests in Rust; live-Ollama tests are `#[ignore]`-gated — merge gates run on MockProvider only
# Test tiers are invariant: unit / integration / e2e (not a stack parameter)

## Notes / constraints
- Two Cargo workspaces: root `crates/` (native) and `skills/` (wasm32-wasip1 target). Keep them separate.
- 7 crates, acyclic deps; kernel is the sole orchestrator (see docs/superpowers/architecture/pythia-architecture.md + docs/adr/0001..0006).
- No EventStore/SkillExecutor traits — single implementers, YAGNI (plan §0). Provider IS a trait (Ollama impl first).
- Event log: replay rule enforced as DB CHECK + immutability triggers; per-event transactions (never per-turn).
- Capabilities: manifest = request, policy = authority, fail-closed default; zero WASI ambient authority. Security requirements SR-1..17 in docs/superpowers/security/pythia-threat-model.md — P0s are owned by named tasks in the plan.
- BYO-key only for providers; never subscription auth.
- Build plan: docs/superpowers/plans/pythia-vertical-slice.md; issue map: docs/superpowers/plans/pythia-vertical-slice-tracking.md.
