package core

import "testing"

func TestNewID_TwoCalls_ProducesDistinctNonEmptyIDs(t *testing.T) {
	a := NewID()
	b := NewID()

	if a == "" || b == "" {
		t.Fatalf("expected non-empty IDs, got %q and %q", a, b)
	}
	if a == b {
		t.Fatalf("expected distinct IDs, got two equal IDs: %q", a)
	}
	// 16 random bytes hex-encoded => 32 hex characters.
	if len(a) != 32 {
		t.Errorf("expected 32-character hex ID, got %d characters: %q", len(a), a)
	}
}
