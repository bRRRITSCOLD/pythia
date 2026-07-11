# 0006 — wasm32-wasip1 (WASI preview 1) as the skill target for the slice; component model deferred

**Status:** Accepted
**Date:** 2026-07-10
**Related:** `docs/superpowers/specs/2026-07-10-pythia-engine-design.md` §4 (Unit 4), §6, §8 (open
question 1)

## Context

The spec leaves this open explicitly: "WASI preview1 vs preview2 / component model for skills —
affects how imports are declared" (§8). It is not blocking for the architecture phase but is a real
load-bearing choice for the capability host's design (ADR-0003), since the shape of "how imports are
declared" directly determines how simple or complex the manifest → policy → Linker pipeline is.

The slice's scope (§6) is narrow and known: **1–2 hand-written Rust skills**, authored by the same
team in the same repo. Self-authoring (agent writes/compiles/promotes its own skills) is an
explicitly deferred milestone — not a concern this decision needs to design for yet.

The capability host's core safety test (ADR-0003, spec §7) is: "a skill requesting `net` without a
grant must fail to instantiate the `net` import; assert the host function is absent." This test needs
to be simple to write, simple to reason about, and directly traceable to the manifest/policy model
described in the spec (flat capability strings like `fs:read:/notes`, `net:smtp`,
`secret:SMTP_PASSWORD`).

## Decision

Target **`wasm32-wasip1`** (WASI preview 1, the flat-namespace ABI, stable in the Rust toolchain) for
skills in this slice. Skills are linked via `wasmtime::Linker<T>` with one host function per granted
capability, matching the manifest vocabulary in `pythia-manifest` directly — each capability *is* an
importable host function; there is no intermediate typed-interface layer.

The **WASI preview 2 / component model** (WIT interfaces, typed worlds, `cargo-component`,
`wit-bindgen`) is explicitly deferred. Revisit this decision at the self-authoring milestone, when
multiple independently-produced skills (agent-authored, possibly many, evolving independently) make
the component model's stronger, versioned interface contracts worth the added toolchain machinery.

## Consequences

**+**
- Direct fit to the design: "manifest requests, policy grants, host links only what's granted" is
  naturally a flat set of named host functions under `wasip1`. The import-absence test is simple to
  write and simple to reason about — no typed-world indirection to reason through.
- Smaller toolchain surface for the slice's hand-written skills: no `wit-bindgen`, no
  `cargo-component`, no adapter modules — consistent with KISS/YAGNI for a first vertical slice with
  exactly 1–2 skills authored by one team.
- `wasmtime`'s `wasip1` support is its most mature, best-documented embedding path, minimizing
  integration risk while the rest of the engine (kernel, event log) is also being built for the first
  time.

**−**
- `wasip1`'s flat namespace and coarser typing mean capability declarations are host-defined string
  conventions (e.g. `"fs:read:/notes"`) rather than a compiler- or WIT-checked typed interface —
  manifest/policy correctness is a **runtime** concern, verified by `pythia-manifest` and
  `pythia-capability-host` parsing and matching logic, not a build-time-checked one. A malformed
  capability string is a runtime error, not a compile error.
- This decision has a known expiration condition, not an indefinite one: once skills are no longer
  all hand-written by one team in one repo (the self-authoring milestone), the component model's
  typed worlds and stricter interface versioning are very likely the better fit for composing
  independently-produced skills safely. This ADR should be explicitly revisited then, not silently
  carried forward by default.
