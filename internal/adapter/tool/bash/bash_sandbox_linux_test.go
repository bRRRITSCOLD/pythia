//go:build linux

package bash

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bRRRITSCOLD/pythia/internal/adapter/tool/bash/sandbox"
)

// TestMain lets this test binary act as its own re-exec target: the
// sandbox spine always re-execs the current binary via /proc/self/exe,
// which under `go test` is this very test binary, not cmd/pythia. This
// mirrors exactly what cmd/pythia/main.go's one-line reserved-subcommand
// hook does in production (same pattern used by the sandbox package's own
// spine_linux_test.go).
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == sandbox.ChildSubcommand {
		os.Exit(sandbox.RunChild())
	}
	os.Exit(m.Run())
}

// TestBash_SandboxedEchoThroughInvoke_Succeeds drives a real command all
// the way through Invoke with the sandbox on: bashTool builds a
// sandbox.Policy from its own workDir and hands it to the re-exec spine
// (T5), which applies Landlock (T6) and then seccomp (T7, #103) before
// exec'ing into bash. Now that T7 has replaced applySeccomp's fail-closed
// stub with a real allowlist filter, a benign command must actually run
// and produce output rather than being refused up front (ADR-0005 §5).
func TestBash_SandboxedEchoThroughInvoke_Succeeds(t *testing.T) {
	dir := t.TempDir()
	tool := New(dir, 5*time.Second, 1<<20, true)

	raw, err := tool.Invoke(context.Background(), json.RawMessage(`{"command":"echo sandboxed-hello"}`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}

	var envelope struct {
		Error string          `json:"error"`
		OK    json.RawMessage `json:"ok"`
	}
	if jsonErr := json.Unmarshal(raw, &envelope); jsonErr != nil {
		t.Fatalf("decode envelope: %v (raw=%s)", jsonErr, raw)
	}
	if envelope.Error != "" {
		t.Fatalf("error = %q, want a success envelope (raw=%s)", envelope.Error, raw)
	}
	if !strings.Contains(string(envelope.OK), "sandboxed-hello") {
		t.Fatalf("ok = %s, want it to contain command output %q", envelope.OK, "sandboxed-hello")
	}
}
