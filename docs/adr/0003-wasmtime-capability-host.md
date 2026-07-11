# 0003 — wasmtime as the capability host / skill sandbox

**Status:** Accepted
**Date:** 2026-07-10
**Related:** `docs/superpowers/specs/2026-07-10-pythia-engine-design.md` §2, §4 (Unit 3), §5;
`docs/reference/hermes-security-architecture.md` (lines ~21, ~250, ~284–285, ~455)

## Context

Spec weakness 1 (safety), and the sharpest line in the Hermes security review: **"The only security
boundary against an adversarial LLM is the operating system."** Confirmed in the reference material:
terminal-backend isolation in Hermes sandboxes shell and file ops only; it "does NOT confine Python
code, MCP subprocesses, or plugin loading" — those "load with full agent privileges." The only fix
available to a Hermes deployment is *whole-process wrapping* (Docker-around-everything, NVIDIA
OpenShell) — an operational choice bolted on top of an architecture that does not require it, and one
easy to skip (a `local` backend exposed to a gateway is explicitly called "untrusted-code-execution-
as-a-service to the internet" in that review).

Pythia's thesis (spec §2) is that Rust + wasmtime **collapses safe and durable into one mechanism**:
WASM has no ambient authority (a skill cannot touch net/fs/secrets unless the host explicitly links
an import — capability-based *by construction*), and WASM execution is deterministic, which is the
precondition the replay guarantee in ADR-0002 depends on.

## Decision

Use **wasmtime**, embedded directly inside `pythia-capability-host`, as the sole execution engine for
skills. Skills compile to `wasm32-wasip1` (ADR-0006) and are instantiated **per call** — no
long-lived skill process, no shared mutable skill state across calls.

The host builds a `wasmtime::Linker` per invocation from two inputs:
- **Skill manifest = request** — the capabilities the skill declares it wants
  (`fs:read:/notes`, `net:smtp`, `secret:SMTP_PASSWORD`, …), schema owned by `pythia-manifest`.
- **Kernel policy = authority** — grant / deny / prompt per capability, evaluated by the host.

The Linker links **only** capabilities that are both requested and granted. A skill with no `net`
grant has no `net` host function in its module's import table — the safety property is "the import
is absent," not "the import exists and returns a permission error." This is the exact assertion the
spec's capability-host test requires (§7): "assert the host function is absent, not merely erroring
at call time."

Taint tracking is a host responsibility layered on top of this: content originating from a tainted
source (web, inbound message, any untrusted origin) is flagged in the event log, and a tainted value
reaching a high-privilege tool requires an explicit policy gate — the LLM can *request* a capability;
only the host's policy decides whether it is *granted*.

## Consequences

**+**
- Closes the exact gap the Hermes review names as the critical structural weakness: in-process code
  (skills) cannot run with ambient full-agent privilege, because there is no ambient privilege to
  inherit — WASM linear memory has none by default.
- The safety guarantee holds without requiring OS-level whole-process wrapping. Unlike Hermes' local
  backend (unsafe to expose without an external container), Pythia's primary safety mechanism is
  in-process and applies uniformly regardless of deployment target.
- One mechanism buys two NFRs: WASM's deterministic execution is also the precondition for the
  replay correctness ADR-0002 depends on — an architectural leverage point, not a coincidence.
- wasmtime is the most mature, actively maintained (Bytecode Alliance), widely embedded WASM runtime
  with a first-class Rust embedding API and strong WASI support — low integration risk for a Rust
  host.
- Per-call instantiation keeps the trust boundary simple to reason about: no skill can accumulate
  privileged state across calls that a later, more-trusted call might accidentally read.

**−**
- WASM sandboxing is not an absolute, formally-proven boundary — Cranelift JIT / runtime
  implementation bugs are a real (if rare) historical class of WASM-runtime escape; this ADR accepts
  wasmtime's maturity and audit history as sufficient for the slice, not as a claim of unconditional
  safety.
- wasmtime is a substantial dependency (JIT compiler, sizeable crate, its own platform-support
  surface) — real build-time and binary-size cost versus a lighter interpreter or no sandbox at all.
- Skill authors are constrained to what compiles to `wasm32-wasip1`: no arbitrary native library
  linking, limited threading without additional WASI proposals at this preview level. Accepted as an
  explicit scope tradeoff for hand-written skills in the slice (ADR-0006).
- wasmtime alone does not solve taint tracking or policy semantics — those remain host logic on top
  of the sandbox and must be tested independently (the import-absence test proves capability
  isolation; it does not prove the policy engine itself is correct).
- **Capability presence is not capability-argument safety.** A skill that legitimately holds
  `net:smtp` and is manipulated by injected content into acting against the wrong target (wrong
  recipient, wrong path within a granted directory) is not a sandbox failure — the sandbox performed
  exactly as designed. This ADR's guarantee is scoped to *ambient/ungranted* authority only; argument-
  level policy, taint-aware gating, fail-closed defaults for capabilities the policy is silent on, and
  host-function/WASI-FFI hardening (path traversal, symlink TOCTOU, preopen scope) are specified as a
  second, explicit layer in `docs/superpowers/security/pythia-threat-model.md` and must not be assumed
  to fall out of wasmtime for free.
