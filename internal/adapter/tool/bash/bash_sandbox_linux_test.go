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

// TestBash_SandboxedEchoThroughInvoke_ReturnsOkEnvelope drives a real
// command all the way through Invoke with the sandbox on: bashTool builds a
// sandbox.Policy from its own workDir, hands it to the re-exec spine (T5),
// which applies Landlock (T6) and seccomp (T7) before execve'ing into
// /bin/bash — and the result still comes back as the frozen, unchanged
// output envelope (SR-3a.10, integration-tier per principles-tdd since it
// exercises the real OS spine rather than a fake).
func TestBash_SandboxedEchoThroughInvoke_ReturnsOkEnvelope(t *testing.T) {
	dir := t.TempDir()
	tool := New(dir, 5*time.Second, 1<<20, true)

	raw, err := tool.Invoke(context.Background(), json.RawMessage(`{"command":"echo sandboxed-hello"}`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}

	r := decodeOK(t, raw)
	if strings.TrimSpace(r.OK.Stdout) != "sandboxed-hello" {
		t.Errorf("stdout = %q, want sandboxed-hello (raw=%s)", r.OK.Stdout, raw)
	}
	if r.OK.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0 (raw=%s)", r.OK.ExitCode, raw)
	}
}
