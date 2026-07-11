package core

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMessage_JSONRoundTrip_PreservesToolCalls(t *testing.T) {
	original := Message{
		ID:        "msg-1",
		SessionID: "sess-1",
		Role:      RoleAssistant,
		Content:   "",
		ToolCalls: []ToolCall{
			{
				ID:   "call-1",
				Name: "bash",
				Args: json.RawMessage(`{"cmd":"ls"}`),
			},
		},
		ToolCallID: "",
		CreatedAt:  time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC),
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var roundTripped Message
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if roundTripped.ID != original.ID {
		t.Errorf("ID: got %q, want %q", roundTripped.ID, original.ID)
	}
	if roundTripped.SessionID != original.SessionID {
		t.Errorf("SessionID: got %q, want %q", roundTripped.SessionID, original.SessionID)
	}
	if roundTripped.Role != original.Role {
		t.Errorf("Role: got %q, want %q", roundTripped.Role, original.Role)
	}
	if len(roundTripped.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(roundTripped.ToolCalls))
	}
	got := roundTripped.ToolCalls[0]
	want := original.ToolCalls[0]
	if got.ID != want.ID || got.Name != want.Name {
		t.Errorf("ToolCall: got %+v, want %+v", got, want)
	}
	if string(got.Args) != string(want.Args) {
		t.Errorf("ToolCall.Args: got %s, want %s", got.Args, want.Args)
	}
	if !roundTripped.CreatedAt.Equal(original.CreatedAt) {
		t.Errorf("CreatedAt: got %v, want %v", roundTripped.CreatedAt, original.CreatedAt)
	}
}

func TestRole_Constants_MatchExpectedStringValues(t *testing.T) {
	cases := map[Role]string{
		RoleSystem:    "system",
		RoleUser:      "user",
		RoleAssistant: "assistant",
		RoleTool:      "tool",
	}
	for role, want := range cases {
		if string(role) != want {
			t.Errorf("Role %v: got %q, want %q", role, string(role), want)
		}
	}
}

func TestSentinelErrors_HaveDescriptiveMessages(t *testing.T) {
	if ErrSessionNotFound == nil || ErrSessionNotFound.Error() != "session not found" {
		t.Errorf("ErrSessionNotFound: got %v", ErrSessionNotFound)
	}
	if ErrMaxIterations == nil || ErrMaxIterations.Error() != "max tool-call iterations exceeded" {
		t.Errorf("ErrMaxIterations: got %v", ErrMaxIterations)
	}
}
