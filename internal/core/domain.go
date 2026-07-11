// Package core holds Pythia's domain vocabulary and ports. It depends on the
// standard library only (docs/adr/0004) — no third-party packages, no
// internal/adapter/* imports — so every adapter and the turn loop can bind to
// it without pulling in infrastructure concerns.
package core

import (
	"encoding/json"
	"time"
)

// Role is the author of a Message. Values map cleanly onto both the Ollama
// dialect and a stricter future dialect (Codex).
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is the model's request to invoke one tool during an assistant turn.
type ToolCall struct {
	ID   string          // provider-assigned id; correlates the eventual tool result
	Name string          // registered tool name
	Args json.RawMessage // JSON arguments; validated at the tool adapter boundary
}

// Message is one entry in a session's conversation history. It is the wire
// format between core and every adapter, and the persisted record. A single
// struct carries all roles; unused fields are zero for a given role.
type Message struct {
	ID         string     // stable id (adapter- or core-assigned)
	SessionID  string     // owning session
	Role       Role       // author
	Content    string     // text content; may be empty when an assistant turn is tool-calls-only
	ToolCalls  []ToolCall // set when Role == RoleAssistant and the model requested tools
	ToolCallID string     // set when Role == RoleTool: which ToolCall.ID this result answers
	CreatedAt  time.Time  // ordering key within a session
}

// Session is a single conversation thread.
type Session struct {
	ID        string
	Title     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ToolSchema is a tool's self-description, advertised to the Provider so the
// model knows what it may call. Parameters is a JSON-Schema object.
type ToolSchema struct {
	Name        string          // unique tool name the model invokes
	Description string          // natural-language purpose for the model
	Parameters  json.RawMessage // JSON Schema (draft) describing the args object
}
