package sandbox

import "testing"

func TestPolicy_ZeroValue_HasEmptyRoots(t *testing.T) {
	var p Policy

	if p.WorkspaceRoot != "" {
		t.Errorf("zero-value Policy.WorkspaceRoot = %q, want empty", p.WorkspaceRoot)
	}
	if p.TmpDir != "" {
		t.Errorf("zero-value Policy.TmpDir = %q, want empty", p.TmpDir)
	}
}
