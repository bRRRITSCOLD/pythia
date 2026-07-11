# Pythia — Agent Engine Design

**Date:** 2026-07-10
**Status:** Approved (brainstorm) → feeds architecture + implementation plan
**Author:** recovered from prior brainstorm (`ai` repo session `cb0e389d`, 2026-07-09) + this session

---

## 1. Problem statement

Autonomous agent frameworks that you can safely leave running unattended do not exist in a
defensible form. The reference point, Nous Research's **Hermes Agent** (analyzed in
`docs/reference/hermes-*.md`), is capable but carries three structural weaknesses its own
documentation admits:

1. **Safety** — "the only security boundary against an adversarial LLM is the operating system."
   Self-authored skills, memory, and cron form a persistence/backdoor triad. Prompt injection →
   shell execution is the headline, accepted-as-residual risk. In-process Python/MCP/plugins run
   with full agent privilege; container backends confine the shell but not in-process code.
2. **Durability** — the agent loop is in-memory. Crash mid-task = lost work. No replay, no
   time-travel debug.
3. **Cost** — model-agnostic by *survival requirement*, but does not route by cost/difficulty.
   Always-on frontier inference does not pencil ($6.57 for one interactive session; an always-on
   loop is multiples/day).

**Pythia** is a Rust agent engine that treats all three as first-class, by-construction design
constraints rather than patches.

## 2. Thesis

> **safe + durable + cheap** — "the agent engine you can actually afford to leave running."

The structural bet: **Rust + `wasmtime` collapses safe and durable into one mechanism.**
- **Safe** — WASM has no ambient authority. A skill cannot touch net/fs/secrets unless the host
  explicitly links an import. Capability-based *by construction*, not bolted on. Injection can
  *ask* for a capability; it cannot *grant* itself one.
- **Durable** — WASM execution is deterministic → replayable. The sandbox guarantee and the replay
  guarantee come from the same runtime.

This is the bet Hermes **cannot copy** without a rewrite — their whole model is "run Python, OS is
the only boundary." Pythia is the opposite by construction.

## 3. System / user outcomes

- A turn survives a mid-turn crash: on restart the kernel replays the event log, re-executes
  **nothing** that already has a recorded result, and continues from the log tip.
- An injected instruction that asks a skill to exfiltrate data over the network **cannot execute**
  when the skill was not granted a `net` capability — the host function is absent from the module.
- The hot loop runs on a local model (Ollama qwen3.5) at zero marginal cost during development;
  the provider seam lets frontier models slot in later without touching the loop.

## 4. Architecture — four units

Each unit has one purpose, a defined interface, and is independently testable.

```
┌─────────────────────────────────────────────────┐
│  CLI channel (one input surface)                 │
└───────────────┬─────────────────────────────────┘
                │ command
        ┌───────▼────────┐
        │  KERNEL         │  event-sourced agent loop
        │  (Rust)         │  every step → append to event log
        └───┬────────┬────┘
            │        │
    ┌───────▼──┐  ┌──▼─────────────────┐
    │ Provider │  │  Capability Host    │  grants imports to WASM
    │ (trait;  │  │  (fs/net/secret     │  per-skill manifest + policy
    │  Ollama) │  │   gated per call)   │
    └──────────┘  └──┬──────────────────┘
                     │ instantiate
              ┌──────▼───────┐
              │ wasmtime      │  skill runs here
              │ sandbox       │  NO ambient authority
              └───────────────┘
```

### Unit 1 — Kernel
Reads a CLI command → asks the provider → dispatches tool/skill → journals every event → resumes
from the log on crash. **Owns durability and context discipline** (it decides which compacted
slice of the log feeds each LLM call — this is the cost lever, not just a durability feature).

### Unit 2 — Event log
Append-only, one table in SQLite (WAL mode).

```
seq | turn_id | type          | payload (json)              | effect_result (json) | tainted | created
----+---------+---------------+-----------------------------+----------------------+---------+--------
1   | t1      | UserCommand   | {text:"summarize..."}       | null                 | 0       | ...
2   | t1      | LlmResponse   | {tool:"read_file",args:{}}  | null                 | 0       | ...
3   | t1      | ToolResult    | {tool:"read_file"}          | {output:"..."}       | 1       | ...
```

**Replay rule:** an event with a recorded `effect_result` is a *fact*, never re-run. Only events
past the log tip execute. That is how a non-deterministic LLM + side-effecting tools become
crash-safe without replaying the outside world.

**Store choice:** SQLite/WAL — single file, atomic appends, queryable. The SQL surface is reused
later for cost rollups and time-travel debugging. Same zero-ops philosophy Hermes validated with
`state.db`.

### Unit 3 — Capability Host
The wasmtime embedder. **Owns safety.** Two-part authority model:
- **Skill manifest = request.** Each skill ships a manifest declaring the capabilities it wants
  (`fs:read:/notes`, `net:smtp`, `secret:SMTP_PASSWORD`, ...).
- **Kernel policy = authority.** A policy file decides grant / deny / prompt per capability.
- The host links **only** the approved capabilities as WASM imports. A skill with no `net` grant
  literally has no network host function in its module.

**Taint tracking:** content from web / inbound message / any tainted source is flagged `tainted=1`
in the event log. Tainted data reaching a high-privilege tool requires a policy gate.

**Key invariant:** the LLM and inbound content are **untrusted**. They can *request* a tool; the
Host's policy — not the LLM — decides whether the capability is granted.

### Unit 4 — Skill runtime
Self-authored-later, hand-written-now. For the first slice: 1–2 skills authored in Rust, compiled
to `wasm32-wasi`, loaded by the Host capability-gated. **Self-authoring (agent emits skill source →
compile → quarantine → promote) is a deferred milestone**, not in the slice.

### Provider seam (Axis A)
A `Provider` trait: `request(messages, tools) -> stream of (text | tool_call)`. First concrete impl
targets **Ollama's OpenAI-compatible** `/v1/chat/completions` endpoint → qwen3.5 local. Anthropic /
OpenRouter / OpenAI impls slot behind the same trait later.

**Locked constraint:** Pythia is **BYO-key / provider-agnostic and never depends on subscription
auth.** (Subscription OAuth tokens are licensed for Claude Code only; pointing a raw API client at
one is a ToS violation. An optional future *path B* — spawning the `claude`/Codex CLI as a single
coarse-grained tool, journaled as one effect — is explicitly out of scope for the slice.)

## 5. Data flow — one turn, journaled

```
User: "summarize my notes.txt and email me the result"
   1. Kernel appends  E1: UserCommand{text}
   2. Kernel → Provider (history rebuilt from log)
      appends          E2: LlmResponse{tool: read_file, args: notes.txt}
   3. Policy check: read_file needs fs:read:notes.txt → Host grants cap, runs skill
      appends          E3: ToolResult{read_file, output:"..."}   ← effect recorded, tainted=1
   4. Kernel → Provider again (E1..E3 as context)
      appends          E4: LlmResponse{tool: send_email, ...}
   5. Policy check: send_email needs net:smtp + content is TAINTED → gate → (allow/deny)
      appends          E5: ToolResult{send_email, ...}
   6. appends          E6: TurnComplete
```

**Crash-resume:** kernel dies after E3. Restart → read log → E3 already has a result → do **not**
re-read the file → feed E1..E3 to the provider → continue from step 4. Effect never
double-executed.

## 6. Scope

### First vertical slice (next `/deliver` session) — thin thread, all four units real
Must prove the thesis end-to-end, shallowly:
1. **Durability demo** — CLI turn → kernel loop → event log → kill mid-turn after a `read_file`
   effect → restart → replays E1..E3, does not re-read, continues. (Section 3 of the original
   brainstorm that never got written.)
2. **Safety demo** — a turn where injected/tool-suggested content asks a skill to `curl | exfil`;
   the skill has no `net` capability → the import is absent → it cannot execute.

Includes: CLI channel, kernel loop, SQLite event log + replay, capability host + wasmtime, manifest
+ policy, 1–2 hand-written Rust→WASM skills, `Provider` trait + Ollama impl.

### Out of scope (YAGNI for the slice)
- Self-authoring skill loop (agent writes/compiles/promotes skills)
- Semantic memory / vector recall (sqlite-vec + FTS5 + reranker)
- Multi-tenant / per-principal isolation
- Agent graph depth > 1 (persistent subagent pools, typed inter-agent contracts)
- Messaging gateway, cron / scheduler
- Path-B CLI-wrapping for subscription auth
- Tiered cost routing beyond the single Ollama provider (the *seam* exists; the router does not)

## 7. Testing approach

- **Kernel/replay** — unit tests over the event log: given a log truncated at each event boundary,
  assert resume re-executes zero recorded effects and produces the same continuation.
- **Capability host** — a skill requesting `net` without a grant must fail to instantiate the net
  import; assert the host function is absent, not merely erroring at call time.
- **Provider** — Ollama impl tested against a local qwen3.5; contract test on the trait so future
  providers are drop-in.
- **End-to-end** — the two slice demos (durability, safety) as integration tests.

## 8. Open questions (for architecture phase, not blocking)

- WASI preview1 vs preview2 / component model for skills — affects how imports are declared.
- Policy file format (TOML vs a small DSL) and whether `prompt` grants block the loop on CLI input.
- Exact compacted-context algorithm (what slice of the log feeds each call) — a cost concern to be
  specified when the router is designed, deferred past the slice.

## 9. Provenance

Recovered from the `ai`-repo brainstorm (2026-07-09): thesis, Rust+wasmtime bet, four-unit
architecture, event-log replay rule, provider/subscription constraint, and the seven differentiation
vectors vs. Hermes. The three `docs/reference/hermes-*.md` breakdowns are the weakness map this
design attacks.
