package core

import "context"

// ChatRequest is one model turn: the full history plus the tools the model may
// call. Providers are stateless w.r.t. history — core always sends the whole
// conversation, so a stricter dialect can re-encode it however it needs.
type ChatRequest struct {
	Messages []Message
	Tools    []ToolSchema
}

// StreamEvent is one incremental output of a single Chat turn. Exactly one of
// {TextDelta present, ToolCalls present + Done, Done, Err} is meaningful per
// event. The stream is ordered: zero or more TextDelta events, then a single
// terminal event (Done, optionally carrying ToolCalls, or Err).
type StreamEvent struct {
	TextDelta string     // a chunk of assistant text to render live
	ToolCalls []ToolCall // delivered on the terminal event when the model requests tools
	Done      bool       // terminal event of the turn; channel closes after
	Err       error      // set on a mid-stream fatal error (terminal)
}

// Provider is the port to an LLM. The sole first-slice impl is Ollama
// (qwen3.5). A future Codex impl binds this exact interface with NO core change.
//
// Contract:
//   - Chat streams one assistant turn over the given history and tool schemas.
//   - The returned channel is closed by the provider after a terminal event.
//   - A connection/setup failure (e.g. Ollama down) is returned as the error;
//     a mid-stream failure arrives as StreamEvent.Err. Core handles both
//     gracefully (surface, do not crash).
//   - ctx cancellation aborts the turn and closes the channel.
//   - A non-streaming provider satisfies the port by emitting one terminal
//     event carrying the full text and/or tool calls.
type Provider interface {
	Chat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
}
