# 0005 — Provider trait with BYO-key HTTP providers, Ollama OpenAI-compatible first

**Status:** Accepted
**Date:** 2026-07-10
**Related:** `docs/superpowers/specs/2026-07-10-pythia-engine-design.md` §2, §4 (Provider seam);
`docs/reference/hermes-systems-architecture.md` §8, ADR-001

## Context

Spec weakness 3 (cost): Hermes is model-agnostic "by survival requirement" — a real, validated
two-axis split (provider identity vs. wire protocol, Hermes' own ADR-001, described as "the single
most important architectural property of the codebase") — but it does not route by cost or
difficulty. Always-on frontier inference does not pencil for an unattended loop (spec cites $6.57 for
one interactive session; an always-on loop is multiples of that per day).

Separately, a locked constraint: Pythia is **BYO-key / provider-agnostic and never depends on
subscription auth.** Subscription OAuth tokens (e.g. those issued for a vendor's own CLI product) are
licensed for that product only; pointing a raw API client at one is a ToS violation. A future
coarse-grained "spawn the vendor CLI as one tool, journal it as one effect" path is explicitly
out of scope for this slice.

The system outcome (spec §3) requires the hot loop to run at zero marginal cost on a local model
during development, with the provider seam allowing frontier models to slot in later "without
touching the loop."

## Decision

Define a single async `Provider` trait in `pythia-provider`:

```
request(messages, tools) -> stream of (text | tool_call)
```

with no concrete HTTP logic in the trait crate. The kernel depends only on this trait (Dependency
Inversion at a real boundary — see the architecture summary for why this trait is justified today
while `EventStore`/`SkillExecutor`-style port traits are not: multiple providers are a genuine,
near-term requirement, not a hypothetical one).

The first and only concrete implementation for the slice is `pythia-provider-ollama`, targeting
**Ollama's OpenAI-compatible `/v1/chat/completions` endpoint** against a local qwen3.5 model.

All current and future implementations are required to authenticate via **direct, user-supplied API
keys or local endpoints (BYO-key)** — no implementation may wrap a subscription-authenticated CLI or
an OAuth token scoped to another product's exclusive use. This is a hard constraint on the trait's
implementations, not a default that can be quietly relaxed by a future provider crate.

## Consequences

**+**
- The kernel is fully decoupled from any specific model vendor or wire dialect; adding a second
  provider (Anthropic, OpenRouter, OpenAI) later is additive — a new crate implementing the trait —
  with zero kernel changes.
- Zero marginal cost during development and for the durability/safety demos: the hot loop runs
  entirely against a local Ollama instance.
- The BYO-key constraint keeps Pythia legally clean and provider-agnostic *by construction*, not by
  ongoing discipline — it removes a foreseeable ToS trap (subscription-token misuse) from the design
  space entirely rather than relying on future contributors to remember not to do it.
- The seam exists now even though the router does not (spec §6, explicitly out of scope) — future
  cost-tiered routing is a kernel-level policy change that selects *which* `Provider` to call, not a
  trait redesign.

**−**
- The trait's shape (`stream of text | tool_call`) fits one class of wire dialect
  (request/response chat-completions-style). Hermes' own ADR-001 documents a leak in exactly this
  kind of abstraction: a transport that "owns the loop" (their Codex app-server transport) breaks the
  uniform assumption and forced a whole extra subsystem to route around it. A future provider with a
  materially different execution model would strain this trait and might eventually need Hermes'
  two-axis split (identity vs. wire shape). That split is deliberately deferred (YAGNI) until a
  second real wire dialect exists — manufacturing it now, with one implementation, would be a layer
  that adds no logic.
- Committing to OpenAI-compatible chat completions as the first wire shape means Ollama-specific
  quirks must be isolated inside `pythia-provider-ollama` and not allowed to leak into the trait's
  vocabulary, or the next implementation will inherit them.
