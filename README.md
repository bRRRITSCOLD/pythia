# Pythia

**A safe, durable, cheap agent engine — the agent you can afford to leave running.**

Pythia runs LLM-driven agent "turns" where every tool the agent invokes is a WebAssembly
skill executing inside a `wasmtime` sandbox with **no ambient authority**. A skill can only
touch the filesystem, network, or secrets that a policy file explicitly grants it. Every step
of a turn is journalled to an append-only SQLite event log, so if the process is killed
mid-turn it resumes on restart by **replaying the log — re-executing nothing that already
happened**.

The bet: Rust + `wasmtime` collapses *safe* and *durable* into one mechanism — no ambient
authority (capabilities are import slots that simply don't exist unless granted) and
deterministic replay (the next action is a pure function of event history).

> **Status: vertical-slice / thin-thread.** The full engine (safe + durable + cheap) is proven
> end-to-end by two integration demos (durability crash-resume, safety exfil-denial), but the
> shipping surface is intentionally minimal. See [Limitations](#limitations) before using it for
> anything real.

## Architecture at a glance

Two Cargo workspaces. The root (`crates/`) is native; `skills/` compiles to `wasm32-wasip1`.

| Crate | Role |
|-------|------|
| `pythia-manifest` | Capability vocabulary + manifest/policy schema; **fail-closed** grant resolution |
| `pythia-eventlog` | SQLite/WAL append-only event store; replay-cursor reads |
| `pythia-provider` / `pythia-provider-ollama` | `Provider` trait (BYO-key) + an Ollama OpenAI-compatible client |
| `pythia-capability-host` | `wasmtime` embedder: per-call scope re-check, fuel/memory/table limits, secret redaction, zero-ambient WASI, `execute()` |
| `pythia-kernel` | Turn-loop state machine; `next_action` is a pure function of event history → replay/resume |
| `pythia-cli` | Composition root + the `pythia` binary |
| `skills/{skill-sdk,read-file,send-email}` | Guest-side SDK + two demo skills (`wasm32-wasip1`) |

Design docs live in `docs/`: [`superpowers/architecture/pythia-architecture.md`](docs/superpowers/architecture/pythia-architecture.md),
[`adr/`](docs/adr), [`superpowers/data/pythia-data-model.md`](docs/superpowers/data/pythia-data-model.md),
[`superpowers/security/pythia-threat-model.md`](docs/superpowers/security/pythia-threat-model.md),
[`superpowers/plans/pythia-vertical-slice.md`](docs/superpowers/plans/pythia-vertical-slice.md).

## Prerequisites

- **Rust** (edition 2021+). `rust-toolchain.toml` pins the stable channel **and** declares the
  `wasm32-wasip1` target, so `rustup` installs it automatically on first build — no manual
  `rustup target add` needed. (If you build outside rustup, add the target yourself.)
- **[Ollama](https://ollama.com)** with a chat model, only if you want to drive a *live* turn
  (`pythia run`). Not needed to build or to run the test suite.
  ```
  ollama pull qwen3.5     # or any chat model you prefer
  ```

## Build & test

```
# native workspace
cargo build --workspace
cargo test  --workspace        # 140 tests; live-Ollama tests are #[ignore]-gated

# skills workspace (compiles to wasm32-wasip1)
cargo build --manifest-path skills/Cargo.toml --target wasm32-wasip1
cargo test  --manifest-path skills/Cargo.toml --target x86_64-unknown-linux-gnu
```

The merge/CI gate runs entirely against a scripted `MockProvider` — no network required. The
two `#[ignore]`d tests exercise a live Ollama server; run them explicitly with a model available:

```
cargo test --workspace -- --ignored
```

## Running the CLI

The binary takes one command shape:

```
pythia run "<your instruction>"
```

Configuration is environment-driven (BYO endpoint/key — Ollama is keyless, the default is
loopback, never a hardcoded hosted endpoint):

| Env var | Default | Meaning |
|---------|---------|---------|
| `PYTHIA_DB_PATH` | `pythia.db` | SQLite event-log path (created if absent) |
| `PYTHIA_OLLAMA_BASE_URL` | `http://localhost:11434` | Ollama server base URL |
| `PYTHIA_OLLAMA_MODEL` | *(provider default)* | Chat model name; set this to pick your model |
| `PYTHIA_POLICY_PATH` | *(unset → no grants)* | Path to a policy TOML (see below). **Unset means fail-closed: zero capabilities granted.** |

Example:

```
export PYTHIA_OLLAMA_MODEL=qwen3.5
export PYTHIA_POLICY_PATH=./policy.toml
pythia run "summarize my notes"
```

**Durability in practice:** if the process is killed mid-turn, just run `pythia run "..."`
again against the same `PYTHIA_DB_PATH`. On startup Pythia finds the open turn and resumes it —
replaying the log, re-executing nothing already recorded — *before* accepting the new command.

### Policy file

A policy grants named skills specific capabilities. An **unlisted** capability is denied exactly
like an explicit `deny` (fail-closed). Capability strings: `fs:read:<path>`, `net:<service>`,
`secret:<name>`.

```toml
# policy.toml — grant the read_file skill read access to /tmp only
[skills.read_file]
"fs:read:/tmp" = "grant"

# a skill NOT listed here (or a capability not listed for it) is denied —
# e.g. send_email requesting net:smtp with no grant cannot open a socket.
```

## Limitations (vertical slice)

Be honest about what this build does and doesn't do yet:

- **The `pythia run` binary registers no skills.** The composition root wires the kernel +
  Ollama provider + event log, so a turn will call the LLM and journal/resume correctly, but it
  cannot dispatch tools until skill registration is added to `build_kernel`
  (tracked in issue #41). The two skills are exercised end-to-end **through the integration
  tests** (`crates/cli/tests/demo_durability.rs`, `demo_safety.rs`), which construct the kernel
  directly with skills registered.
- **Providers:** only Ollama (OpenAI-compatible), BYO-key. `build_context` renders tool
  calls/results as text rather than structured `tool_call_id` — fine for Ollama's lenient API,
  but a strict OpenAI-dialect provider needs issue #38 first.
- **Resource safety** bounds CPU (fuel) and memory/table growth, and closes the known fuel-blind
  blocking-hang vectors at the config surface; a general wall-clock watchdog is tracked in
  issue #34. **Read #34/#36 before running untrusted skills unattended.**
- **Secret redaction** is verbatim-substring (a skill that *transforms* a secret before
  returning it is out of reach); **exactly-once** effect execution across a crash is not yet
  guaranteed for non-idempotent capabilities (both fine for the current idempotent demo skills;
  see issues #39).

See the open issues on GitHub and `docs/superpowers/handoffs/` for the current follow-up backlog.

## License

MIT.
