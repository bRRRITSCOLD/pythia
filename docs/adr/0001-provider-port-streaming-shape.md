# 0001 — Provider port with first-class streaming

**Status:** Accepted

## Context

Pythia's core turn loop must reach an LLM, and the LLM must be swappable: the
first-slice impl is local Ollama (qwen3.5) over HTTP, but a subscription Codex
provider must drop in later with **no change to core** (spec resolved decision
4; stack profile). Two forces are in tension:

- Streaming is a product requirement — assistant tokens must appear
  incrementally in the TUI. That pushes streaming into the port itself, not a
  bolt-on.
- The port must also carry structured tool calls and tool results, because the
  turn loop dispatches tools between model invocations. A future stricter
  dialect (Codex) must be able to re-encode the same message history and tool
  schemas.

Shape options considered:

| Option | Strengths | Weaknesses | When to prefer |
|--------|-----------|------------|----------------|
| **A. Blocking `Chat() (Message, error)`** (no streaming) | Simplest signature; trivial to test | Fails the streaming requirement; retrofitting streaming later breaks every caller | Batch/non-interactive tools |
| **B. Streaming via `(<-chan StreamEvent, error)` with a terminal event carrying tool calls** (chosen) | Streaming is first-class; setup errors fail fast, mid-stream errors in-band; non-streaming providers emit one terminal event; tool calls arrive complete at Done | Slightly more machinery than blocking; channel lifecycle must be disciplined | Interactive streaming agent — our case |
| **C. Callback `Chat(ctx, req, func(StreamEvent))`** | No channel lifecycle to manage | Inverts control flow into core; harder to `select` on ctx cancellation; awkward to test | Push-only pipelines |

Ollama's `/api/chat` streams deltas and returns `tool_calls` in the final
message; a non-streaming API can emit a single terminal chunk — both satisfy
option B cleanly.

## Decision

Define a single `Provider` port in `internal/core`:

```go
type Provider interface {
	Chat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
}
```

`ChatRequest` carries the full `[]Message` history and the `[]ToolSchema` the
model may call. `StreamEvent` carries either a `TextDelta`, or a terminal `Done`
event (optionally with `[]ToolCall`), or a terminal `Err`. Connection/setup
failures (Ollama down) return via the immediate `error`; mid-stream failures
arrive as `StreamEvent.Err`. A non-streaming provider satisfies the port by
emitting one terminal event. Messages carry `Role`, `Content`, structured
`ToolCalls`, and `ToolCallID` so a stricter dialect maps cleanly.

Core never imports the Ollama adapter; wiring happens only in `cmd/pythia`.

## Consequences

- **Easier:** streaming is native, so the TUI renders deltas with no buffering;
  adding Codex is a new adapter implementing the same interface, verified by
  port contract tests rather than requiring a second impl now.
- **Easier:** graceful Ollama-down behavior falls out of the dual error path
  (fail-fast setup + in-band mid-stream) — the NFR is satisfied by the shape.
- **Harder:** channel lifecycle discipline is required — the provider must close
  the channel after exactly one terminal event and honor `ctx` cancellation.
  Enforced by a port contract test.
- **Obligation:** the exact qwen3.5 tool-calling wire format (native vs.
  prompt-encoded) is resolved by the Ollama adapter against the running Ollama;
  it stays entirely behind this port.
