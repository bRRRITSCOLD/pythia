# Spec — Pythia First Vertical Slice (bare agent loop)

## Problem statement

Pythia is a Go AI-agent framework: TUI-first, Pi-simple core (4 tools:
read/write/bash/edit) that extends into hermes-level complexity via
plugins/skills/modules later. Today there is no code. We need the thinnest
end-to-end slice that proves the architecture: a human types into a terminal,
an LLM reasons and calls tools, and the conversation persists across restarts.

The slice must lock in the two extension seams that make Pythia more than a
toy — a **Provider port** (swap Ollama → Codex without touching core) and a
**tool registry** shaped so out-of-process `hashicorp/go-plugin` gRPC tools
drop in later — without building either extension now.

## User / system outcomes

- A user runs a single `go build` binary, gets a Bubble Tea TUI (input box +
  scrolling/streaming output), types a request, and watches the agent respond.
- The agent core runs a turn loop: user message → Provider → (optional tool
  calls → execute → feed results back) → repeat until the model returns a final
  answer with no tool calls.
- Exactly 4 built-in tools are callable by the model: `read`, `write`, `bash`,
  `edit` — all registered through one `ToolRegistry` interface.
- The LLM is reached **only** through a `Provider` port; the sole impl is
  Ollama (qwen3.5) over HTTP, streaming.
- A session and its full message history persist to embedded SQLite
  (`modernc.org/sqlite`, CGO-free). Restarting the binary and resuming the
  session replays prior history.
- Streaming: assistant tokens appear incrementally in the TUI as the model
  emits them.

## In scope

- Bubble Tea TUI: input box, streaming output viewport, minimal status line.
- Agent core / turn loop with tool-call dispatch.
- `Provider` port + Ollama streaming adapter (qwen3.5).
- `ToolRegistry` port + 4 built-in tools (read, write, bash, edit).
- SQLite persistence: sessions + messages (incl. tool calls/results), with a
  repository port and migrations.
- Tool-argument validation at the adapter boundary (go-playground/validator).
- Single self-contained `go build` binary; pure-Go / CGO-free deps only.
- Unit + integration + e2e (teatest) tests per the invariant test tiers.

## Out of scope (YAGNI for this slice)

- Any real plugin: no `go-plugin` gRPC process yet — only the registry *shape*
  that admits one. (Seam, not implementation.)
- Codex provider — only the `Provider` port shape that admits it.
- chromem-go / RAG / agent memory / skills.
- Full bash sandbox (landlock/seccomp/gVisor). See decision below.
- Multi-session management UI, config files beyond env, auth, networking
  beyond the local Ollama HTTP call.

## Resolved design decisions (were open questions)

1. **Turn loop shape** — synchronous turn loop; model may emit ≥0 tool calls
   per turn; core executes them, appends tool results as messages, re-invokes
   Provider; terminates when a turn has no tool calls. Bounded max-iterations
   guard to prevent infinite tool loops.
2. **Streaming** — Provider port exposes a streaming API (channel/callback of
   token deltas + a terminal event carrying any tool calls). TUI renders
   deltas live. Non-streaming providers can satisfy the port by emitting one
   final chunk.
3. **Tool registry shape** — `Tool` = name + JSON-schema description +
   `Invoke(ctx, argsJSON) (resultJSON, error)`. Registry maps name → Tool and
   exposes the schema list to the Provider. Built-in tools implement the same
   interface a future gRPC-plugin proxy will implement, so core is agnostic to
   in-process vs out-of-process. **No `go-plugin` dependency in this slice.**
4. **Provider port shape** — `Provider.Chat(ctx, messages, tools) ->
   stream`. Ollama adapter translates to/from Ollama's `/api/chat` incl. tool
   calling. Messages carry role, content, and structured tool-call /
   tool-result fields so a stricter dialect (Codex) maps cleanly later.
5. **Bash tool sandbox** — **MVP runs bash in a subprocess with: a context
   timeout, a configured working directory, and no inherited secrets beyond
   the parent env.** Full OS sandbox (landlock+seccomp / container) is an
   explicit follow-up, not this slice. The bash tool boundary is isolated so
   the sandbox drops in behind it. Untrusted-content handling (tool output and
   model output are untrusted) is called out for the architecture threat pass.
6. **Persistence boundary** — a `SessionRepository` port owns session +
   message read/write; SQLite adapter behind it. Core never touches SQL.
7. **Config** — environment variables only (Ollama base URL, model, working
   dir), parsed into a validated `Config` value at startup. No config file.

## Acceptance criteria

- `go build ./...` produces one binary with no CGO.
- Launching the binary opens the TUI; typing a prompt that requires reading a
  file (e.g. "what's in go.mod?") makes the agent call `read` and answer.
- A prompt that writes/edits a file persists the change on disk via the tool.
- Killing and relaunching with the same session id replays prior messages.
- Assistant output streams token-by-token.
- `go test ./...` green across unit/integration/e2e tiers.
- Swapping the Provider or adding a tool requires **no** change to agent-core
  files (verified by the interfaces, not a second impl).

## Open questions for the specialists

- Exact Ollama tool-calling wire format for qwen3.5 (native tool calls vs
  prompt-encoded) — architecture/provider task resolves against the running
  Ollama.
- teatest current import path — plan/build task verifies.
- Message schema: single `messages` table with typed columns vs a
  content-blocks table — data phase decides.
