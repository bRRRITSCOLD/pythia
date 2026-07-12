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
	want := "Pythia: hello red world"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("content = %q, contains unsanitized escape byte", got)
	}
}

// TestModel_Submit_EchoesUserMessage verifies the user's own message is
// written into the transcript (labeled "You:") when a turn is submitted —
// without this the chat window only ever shows assistant output and the
// user never sees what they asked (the "past messages don't show" bug).
func TestModel_Submit_EchoesUserMessage(t *testing.T) {
	m := newTestModel()

	next, _ := m.submit("what's in go.mod?")
	m = next.(Model)

	got := m.content.String()
	if !strings.Contains(got, "You: what's in go.mod?") {
		t.Errorf("content = %q, want it to echo the user message", got)
	}
}

// TestModel_Transcript_SeparatesUserFromAssistant verifies distinct blocks
// are newline-separated and role-labeled, so a user message and the
// assistant reply never run together (the "no new line" bug where
// "...today?Pragmatic DDD..." concatenated two messages).
func TestModel_Transcript_SeparatesUserFromAssistant(t *testing.T) {
	m := newTestModel()
	ch := make(chan core.AgentEvent)

	n1, _ := m.submit("hi")
	m = n1.(Model)

	n2, _ := m.handleAgentEvent(agentEventMsg{
		event: core.AgentEvent{Type: core.EventTextDelta, TextDelta: "Hello!"},
		ch:    ch,
	})
	m = n2.(Model)

	got := m.content.String()
	want := "You: hi\n\nPythia: Hello!"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

// TestModel_Transcript_SeparatesConsecutiveAssistantSegments verifies that a
// second assistant text segment (after a tool call within the same turn, or
// a fresh turn) starts its own labeled block instead of concatenating onto
// the previous assistant text.
func TestModel_Transcript_SeparatesConsecutiveAssistantSegments(t *testing.T) {
	m := newTestModel()
	ch := make(chan core.AgentEvent)

	send := func(ev core.AgentEvent) {
		next, _ := m.handleAgentEvent(agentEventMsg{event: ev, ch: ch})
		m = next.(Model)
	}

	send(core.AgentEvent{Type: core.EventTextDelta, TextDelta: "first"})
	send(core.AgentEvent{Type: core.EventToolCallStarted, ToolCall: &core.ToolCall{ID: "c1", Name: "read"}})
	send(core.AgentEvent{Type: core.EventTextDelta, TextDelta: "second"})

	got := m.content.String()
	if strings.Contains(got, "firstsecond") {
		t.Errorf("content = %q, assistant segments concatenated without separation", got)
	}
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Errorf("content = %q, want it to contain both segments", got)
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

// TestModel_ErrorMsg_SanitizesErrorInView verifies that an EventError whose
// message embeds a terminal escape sequence (e.g. an OSC-52 clipboard-write
// payload smuggled through a provider or tool error string) is sanitized
// (SR-1) before reaching View() — closing the gap where every other
// untrusted channel was sanitized except errors.
func TestModel_ErrorMsg_SanitizesErrorInView(t *testing.T) {
	m := newTestModel()
	ch := make(chan core.AgentEvent)
	wantErr := errors.New("provider said \x1b]52;c;ZXZpbA==\x07 boom")

	next, _ := m.handleAgentEvent(agentEventMsg{
		event: core.AgentEvent{Type: core.EventError, Err: wantErr},
		ch:    ch,
	})
	m = next.(Model)

	view := m.View()
	if strings.Contains(view, "\x1b") {
		t.Errorf("View() = %q, contains unsanitized escape byte from error message", view)
	}
	if !strings.Contains(view, "boom") {
		t.Errorf("View() = %q, want it to still contain the error text", view)
	}
}

// TestModel_AgentChannelMsg_SanitizesErrorInStatus verifies that a failure
// from Agent.Send itself (agentChannelMsg.err) is sanitized (SR-1) before
// being written into the status line, mirroring the EventError fix — the
// second untrusted-error code path in Update.
func TestModel_AgentChannelMsg_SanitizesErrorInStatus(t *testing.T) {
	m := newTestModel()
	wantErr := errors.New("dial failed \x1b]52;c;ZXZpbA==\x07 refused")

	next, cmd := m.Update(agentChannelMsg{err: wantErr})
	m = next.(Model)

	if strings.Contains(m.status, "\x1b") {
		t.Errorf("status = %q, contains unsanitized escape byte from Send error", m.status)
	}
	if !strings.Contains(m.status, "refused") {
		t.Errorf("status = %q, want it to still contain the error text", m.status)
	}
	if cmd != nil {
		t.Errorf("cmd = non-nil after Agent.Send error, want nil")
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
