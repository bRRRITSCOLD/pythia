package ollama

import "encoding/json"

// wireRequest is the JSON body sent to Ollama's POST /api/chat.
type wireRequest struct {
	Model    string        `json:"model"`
	Messages []wireMessage `json:"messages"`
	Tools    []wireTool    `json:"tools,omitempty"`
	Stream   bool          `json:"stream"`
}

// wireMessage is one entry in the request's messages array. ToolCalls is set
// for an assistant message that requested tools; ToolCallID is set for a
// RoleTool message answering a specific ToolCall.
type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// wireTool is one entry in the request's tools array — Ollama's function-
// calling wire shape.
type wireTool struct {
	Type     string      `json:"type"`
	Function wireToolDef `json:"function"`
}

type wireToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// wireToolCall is a tool invocation, on either the request side (replaying
// an earlier assistant turn's tool calls) or the response side (the model's
// new tool request). ID may be empty on the response side — Ollama does not
// always assign one.
type wireToolCall struct {
	ID       string           `json:"id,omitempty"`
	Function wireToolCallFunc `json:"function"`
}

type wireToolCallFunc struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// wireResponse is one NDJSON line of a streamed /api/chat response. Content
// accumulates across non-terminal lines; Done marks the terminal line, which
// may also carry ToolCalls.
type wireResponse struct {
	Message wireResponseMessage `json:"message"`
	Done    bool                `json:"done"`
}

type wireResponseMessage struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	ToolCalls []wireToolCall `json:"tool_calls,omitempty"`
}
