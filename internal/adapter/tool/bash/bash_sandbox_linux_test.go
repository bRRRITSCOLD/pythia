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

// TestBash_SandboxedEchoThroughInvoke_FailsClosedUntilSeccompImplemented
// drives a real command all the way through Invoke with the sandbox on:
// bashTool builds a sandbox.Policy from its own workDir and hands it to the
// re-exec spine (T5), which applies Landlock (T6, real) before reaching
// applySeccomp (T7, #103, still a fail-closed stub — see
// sandbox/seccomp_linux.go). Until T7 lands, the production path must
// refuse to run the command rather than presenting as fully sandboxed with
// no syscall filter installed (ADR-0005 §5, SR-3a fail-closed), so this
// locks in a soft error envelope — command never run — rather than a
// misleading success. Flip this back to asserting a success envelope (see
// git history for the prior version) once T7 lands.
func TestBash_SandboxedEchoThroughInvoke_FailsClosedUntilSeccompImplemented(t *testing.T) {
	dir := t.TempDir()
	tool := New(dir, 5*time.Second, 1<<20, true)

	raw, err := tool.Invoke(context.Background(), json.RawMessage(`{"command":"echo sandboxed-hello"}`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}

	var envelope struct {
		Error string `json:"error"`
	}
	if jsonErr := json.Unmarshal(raw, &envelope); jsonErr != nil {
		t.Fatalf("decode envelope: %v (raw=%s)", jsonErr, raw)
	}
	if !strings.Contains(envelope.Error, "sandbox unavailable") {
		t.Fatalf("error = %q, want it to report the sandbox as unavailable (raw=%s)", envelope.Error, raw)
	}
}
