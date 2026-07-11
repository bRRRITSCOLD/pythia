package core

import "encoding/json"

// AgentEventType classifies a UI-facing event from the turn loop.
type AgentEventType int

const (
	EventTextDelta        AgentEventType = iota // assistant token(s) to render
	EventToolCallStarted                        // a tool is about to run
	EventToolCallFinished                       // a tool returned
	EventTurnComplete                           // no more tool calls; turn ended
	EventError                                  // fatal error; loop stopped
)

// String renders the AgentEventType for TUI display and logs.
func (t AgentEventType) String() string {
	switch t {
	case EventTextDelta:
		return "EventTextDelta"
	case EventToolCallStarted:
		return "EventToolCallStarted"
	case EventToolCallFinished:
		return "EventToolCallFinished"
	case EventTurnComplete:
		return "EventTurnComplete"
	case EventError:
		return "EventError"
	default:
		return "EventUnknown"
	}
}

// AgentEvent is what the TUI renders. It hides Provider/Tool details from the
// UI so the TUI adapter can bind to core without ever importing Provider.
type AgentEvent struct {
	Type       AgentEventType  // which kind of event this is
	TextDelta  string          // set when Type == EventTextDelta
	ToolCall   *ToolCall       // set when Type == EventToolCallStarted
	ToolResult json.RawMessage // set when Type == EventToolCallFinished
	Err        error           // set when Type == EventError
}
