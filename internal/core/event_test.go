package core_test

import (
	"testing"

	"github.com/bRRRITSCOLD/pythia/internal/core"
)

// TestAgentEventType_String_CoversAllVariants verifies that every
// AgentEventType constant, in the frozen iota order, renders a distinct,
// non-empty string.
func TestAgentEventType_String_CoversAllVariants(t *testing.T) {
	tests := []struct {
		name string
		in   core.AgentEventType
		want string
	}{
		{"EventTextDelta", core.EventTextDelta, "EventTextDelta"},
		{"EventToolCallStarted", core.EventToolCallStarted, "EventToolCallStarted"},
		{"EventToolCallFinished", core.EventToolCallFinished, "EventToolCallFinished"},
		{"EventTurnComplete", core.EventTurnComplete, "EventTurnComplete"},
		{"EventError", core.EventError, "EventError"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestAgentEventType_IotaOrder_MatchesArchDoc verifies the frozen ordering:
// EventTextDelta, EventToolCallStarted, EventToolCallFinished,
// EventTurnComplete, EventError.
func TestAgentEventType_IotaOrder_MatchesArchDoc(t *testing.T) {
	if core.EventTextDelta != 0 {
		t.Errorf("EventTextDelta = %d, want 0", core.EventTextDelta)
	}
	if core.EventToolCallStarted != 1 {
		t.Errorf("EventToolCallStarted = %d, want 1", core.EventToolCallStarted)
	}
	if core.EventToolCallFinished != 2 {
		t.Errorf("EventToolCallFinished = %d, want 2", core.EventToolCallFinished)
	}
	if core.EventTurnComplete != 3 {
		t.Errorf("EventTurnComplete = %d, want 3", core.EventTurnComplete)
	}
	if core.EventError != 4 {
		t.Errorf("EventError = %d, want 4", core.EventError)
	}
}

// TestAgentEventType_String_UnknownVariantReturnsFallback verifies that a
// value outside the defined range still renders a safe, non-empty string
// instead of panicking or returning an empty label.
func TestAgentEventType_String_UnknownVariantReturnsFallback(t *testing.T) {
	unknown := core.AgentEventType(99)
	got := unknown.String()
	if got == "" {
		t.Error("String() returned empty string for unknown variant")
	}
}

// TestAgentEvent_ZeroValue_HasNilOptionalFields verifies that a zero-value
// AgentEvent carries nil ToolCall/ToolResult/Err so callers can distinguish
// "not set" without extra plumbing.
func TestAgentEvent_ZeroValue_HasNilOptionalFields(t *testing.T) {
	var ev core.AgentEvent

	if ev.ToolCall != nil {
		t.Error("expected zero-value ToolCall to be nil")
	}
	if ev.ToolResult != nil {
		t.Error("expected zero-value ToolResult to be nil")
	}
	if ev.Err != nil {
		t.Error("expected zero-value Err to be nil")
	}
	if ev.Type != core.EventTextDelta {
		t.Errorf("expected zero-value Type to be EventTextDelta, got %v", ev.Type)
	}
}
