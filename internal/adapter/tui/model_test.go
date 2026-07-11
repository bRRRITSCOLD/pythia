package tui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bRRRITSCOLD/pythia/internal/core"
)

// newTestModel returns a ready Model (as if a WindowSizeMsg already
// arrived) bound to no real Agent — the tests here only exercise
// handleAgentEvent, which never calls the agent.
func newTestModel() Model {
	m := NewModel(nil, "session-1")
	m.ready = true
	return m
}

// TestModel_TextDeltaMsg_AppendsSanitizedTextToViewport verifies that an
// EventTextDelta carrying an ANSI escape sequence is sanitized (SR-1)
// before being appended to the transcript, and that plain deltas
// concatenate incrementally (no batching).
func TestModel_TextDeltaMsg_AppendsSanitizedTextToViewport(t *testing.T) {
	m := newTestModel()
	ch := make(chan core.AgentEvent)

	next, _ := m.handleAgentEvent(agentEventMsg{
		event: core.AgentEvent{Type: core.EventTextDelta, TextDelta: "hello \x1b[31mred\x1b[0m"},
		ch:    ch,
	})
	m = next.(Model)

	next2, _ := m.handleAgentEvent(agentEventMsg{
		event: core.AgentEvent{Type: core.EventTextDelta, TextDelta: " world"},
		ch:    ch,
	})
	m = next2.(Model)

	got := m.content.String()
	want := "hello red world"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("content = %q, contains unsanitized escape byte", got)
	}
}

// TestModel_ErrorMsg_ShowsErrorAndStaysUsable verifies that an EventError
// surfaces on the model (status/err) without leaving the model unusable:
// busy is cleared so the user can start a new turn (graceful Ollama-down
// NFR), and the returned tea.Model is still a valid Model.
func TestModel_ErrorMsg_ShowsErrorAndStaysUsable(t *testing.T) {
	m := newTestModel()
	m.busy = true
	ch := make(chan core.AgentEvent)
	wantErr := errors.New("provider unreachable")

	next, cmd := m.handleAgentEvent(agentEventMsg{
		event: core.AgentEvent{Type: core.EventError, Err: wantErr},
		ch:    ch,
	})
	m = next.(Model)

	if m.busy {
		t.Errorf("busy = true after EventError, want false (TUI must stay usable)")
	}
	if !errors.Is(m.err, wantErr) {
		t.Errorf("err = %v, want %v", m.err, wantErr)
	}
	if !strings.Contains(m.View(), "provider unreachable") {
		t.Errorf("View() = %q, want it to contain the error message", m.View())
	}
	if cmd != nil {
		t.Errorf("cmd = non-nil after EventError, want nil (stop listening on this channel)")
	}
}

// TestModel_ToolCallStartedMsg_ShowsToolActivity verifies that an
// EventToolCallStarted updates the status line with the (sanitized) tool
// name so the user sees activity while a tool runs.
func TestModel_ToolCallStartedMsg_ShowsToolActivity(t *testing.T) {
	m := newTestModel()
	ch := make(chan core.AgentEvent)
	call := core.ToolCall{ID: "call-1", Name: "read"}

	next, cmd := m.handleAgentEvent(agentEventMsg{
		event: core.AgentEvent{Type: core.EventToolCallStarted, ToolCall: &call},
		ch:    ch,
	})
	m = next.(Model)

	if !strings.Contains(m.status, "read") {
		t.Errorf("status = %q, want it to mention the tool name %q", m.status, "read")
	}
	if cmd == nil {
		t.Errorf("cmd = nil, want a Cmd re-subscribing to the channel")
	}
}

// TestModel_ToolCallFinishedMsg_SanitizesToolResult verifies that an
// EventToolCallFinished's ToolResult is sanitized (SR-1) before being
// appended to the transcript, even when it embeds a terminal escape
// sequence inside the JSON payload.
func TestModel_ToolCallFinishedMsg_SanitizesToolResult(t *testing.T) {
	m := newTestModel()
	ch := make(chan core.AgentEvent)
	result, err := json.Marshal(map[string]string{"ok": "line1\x1b[31mDANGER\x1b[0mline2"})
	if err != nil {
		t.Fatalf("setup: marshal tool result: %v", err)
	}

	next, _ := m.handleAgentEvent(agentEventMsg{
		event: core.AgentEvent{Type: core.EventToolCallFinished, ToolResult: result},
		ch:    ch,
	})
	m = next.(Model)

	got := m.content.String()
	if strings.Contains(got, "\x1b") {
		t.Errorf("content = %q, contains unsanitized escape byte from tool result", got)
	}
	if !strings.Contains(got, "DANGER") {
		t.Errorf("content = %q, want it to still contain the tool result text", got)
	}
}

// TestModel_TurnCompleteMsg_ClearsBusyAndStopsListening verifies that
// EventTurnComplete clears busy (so the next Enter submits) and returns a
// nil Cmd (the channel is closed by the Agent; nothing left to listen for).
func TestModel_TurnCompleteMsg_ClearsBusyAndStopsListening(t *testing.T) {
	m := newTestModel()
	m.busy = true
	ch := make(chan core.AgentEvent)

	next, cmd := m.handleAgentEvent(agentEventMsg{
		event: core.AgentEvent{Type: core.EventTurnComplete},
		ch:    ch,
	})
	m = next.(Model)

	if m.busy {
		t.Errorf("busy = true after EventTurnComplete, want false")
	}
	if cmd != nil {
		t.Errorf("cmd = non-nil after EventTurnComplete, want nil")
	}
}
