package bash

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/bRRRITSCOLD/pythia/internal/adapter/tool/bash/sandbox"
)

// result mirrors the frozen ok-envelope shape for this tool, used to decode
// and assert on Invoke's JSON output in tests.
type result struct {
	OK struct {
		Stdout    string `json:"stdout"`
		Stderr    string `json:"stderr"`
		ExitCode  int    `json:"exit_code"`
		Truncated bool   `json:"truncated"`
		TimedOut  bool   `json:"timed_out"`
	} `json:"ok"`
}

func decodeOK(t *testing.T, raw json.RawMessage) result {
	t.Helper()
	var r result
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("decode result: %v (raw=%s)", err, raw)
	}
	return r
}

func TestBash_SimpleCommand_ReturnsStdoutAndZeroExit(t *testing.T) {
	tool := New(t.TempDir(), time.Second, 1<<20, false)

	raw, err := tool.Invoke(context.Background(), json.RawMessage(`{"command":"echo hello"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r := decodeOK(t, raw)
	if strings.TrimSpace(r.OK.Stdout) != "hello" {
		t.Errorf("stdout = %q, want hello", r.OK.Stdout)
	}
	if r.OK.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", r.OK.ExitCode)
	}
}

func TestBash_NonZeroExit_ReturnedAsSoftResultNotGoError(t *testing.T) {
	tool := New(t.TempDir(), time.Second, 1<<20, false)

	raw, err := tool.Invoke(context.Background(), json.RawMessage(`{"command":"exit 7"}`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}

	r := decodeOK(t, raw)
	if r.OK.ExitCode != 7 {
		t.Errorf("exit_code = %d, want 7", r.OK.ExitCode)
	}
}

func TestBash_CommandExceedsTimeout_KillsProcessAndFlagsTimedOut(t *testing.T) {
	tool := New(t.TempDir(), 50*time.Millisecond, 1<<20, false)

	start := time.Now()
	raw, err := tool.Invoke(context.Background(), json.RawMessage(`{"command":"sleep 5"}`))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r := decodeOK(t, raw)
	if !r.OK.TimedOut {
		t.Errorf("timed_out = false, want true")
	}
	if elapsed > 4*time.Second {
		t.Errorf("elapsed = %v, want well under the 5s sleep (process should be killed)", elapsed)
	}
}

func TestBash_RunsInConfiguredWorkDir(t *testing.T) {
	dir := t.TempDir()
	tool := New(dir, time.Second, 1<<20, false)

	raw, err := tool.Invoke(context.Background(), json.RawMessage(`{"command":"pwd"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r := decodeOK(t, raw)
	got := strings.TrimSpace(r.OK.Stdout)
	// Resolve symlinks (e.g. macOS /tmp -> /private/tmp) before comparing.
	if !strings.HasSuffix(got, strings.TrimSuffix(dir, "/")) && got != dir {
		t.Errorf("pwd = %q, want it to reference workdir %q", got, dir)
	}
}

func TestBash_OutputExceedsCap_TruncatesAndFlags(t *testing.T) {
	tool := New(t.TempDir(), time.Second, 10, false)

	raw, err := tool.Invoke(context.Background(), json.RawMessage(`{"command":"printf 'abcdefghijklmnopqrstuvwxyz'"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r := decodeOK(t, raw)
	if !r.OK.Truncated {
		t.Errorf("truncated = false, want true")
	}
	if len(r.OK.Stdout) > 10 {
		t.Errorf("stdout len = %d, want <= 10", len(r.OK.Stdout))
	}
}

func TestBash_MalformedArgs_ReturnsErrorEnvelope(t *testing.T) {
	tool := New(t.TempDir(), time.Second, 1<<20, false)

	raw, err := tool.Invoke(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected Go error (malformed args must be a soft error envelope): %v", err)
	}

	var env struct {
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal(raw, &env); jerr != nil {
		t.Fatalf("decode error envelope: %v (raw=%s)", jerr, raw)
	}
	if env.Error == "" {
		t.Errorf("want non-empty error message, got raw=%s", raw)
	}
}

func TestBash_Schema_AdvertisesCommandParam(t *testing.T) {
	tool := New(t.TempDir(), time.Second, 1<<20, false)

	schema := tool.Schema()
	if schema.Name != "bash" {
		t.Errorf("schema.Name = %q, want bash", schema.Name)
	}
	if !strings.Contains(string(schema.Parameters), "command") {
		t.Errorf("schema.Parameters = %s, want it to mention command", schema.Parameters)
	}
}

// TestBash_SandboxOnButUnavailable_ReturnsErrorEnvelopeCommandNotRun locks
// in SR-3a.10: when the sandbox is requested but setup fails (or the
// platform/kernel is unsupported, i.e. sandbox.Run returns
// sandbox.ErrUnsupported or any other setup error), Invoke returns a soft
// error envelope and never falls back to running the command unsandboxed.
// The sandbox runner is faked here (via the package-private runSandboxed
// seam) so this is a deterministic unit test on any GOOS, independent of
// whether the current platform's real sandbox happens to be available —
// the Linux end-to-end sandboxed-echo path is covered separately
// (bash_sandbox_linux_test.go).
func TestBash_SandboxOnButUnavailable_ReturnsErrorEnvelopeCommandNotRun(t *testing.T) {
	tool := New(t.TempDir(), time.Second, 1<<20, true).(*bashTool)
	tool.runSandboxed = func(_ context.Context, _ sandbox.Policy, _ string, _, _ io.Writer) (int, error) {
		return -1, sandbox.ErrUnsupported
	}

	raw, err := tool.Invoke(context.Background(), json.RawMessage(`{"command":"echo should-not-run"}`))
	if err != nil {
		t.Fatalf("unexpected Go error (fail-closed must be a soft error envelope): %v", err)
	}

	var env struct {
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal(raw, &env); jerr != nil {
		t.Fatalf("decode error envelope: %v (raw=%s)", jerr, raw)
	}
	if env.Error == "" {
		t.Errorf("want non-empty error message (command not run), got raw=%s", raw)
	}
	if strings.Contains(string(raw), "should-not-run") {
		t.Errorf("raw=%s looks like the command actually ran; want fail-closed", raw)
	}
}

// TestBash_SandboxOff_EmitsOneTimeUnsandboxedLog locks in SR-3a.11: the
// legacy direct-exec path logs a "sandbox DISABLED" warning exactly once
// per tool instance, no matter how many commands are run through it.
func TestBash_SandboxOff_EmitsOneTimeUnsandboxedLog(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	tool := New(t.TempDir(), time.Second, 1<<20, false)

	for i := 0; i < 3; i++ {
		if _, err := tool.Invoke(context.Background(), json.RawMessage(`{"command":"true"}`)); err != nil {
			t.Fatalf("unexpected error on invocation %d: %v", i, err)
		}
	}

	logged := buf.String()
	count := strings.Count(logged, "bash sandbox DISABLED")
	if count != 1 {
		t.Errorf("logged %q %d times, want exactly 1; log output:\n%s", "bash sandbox DISABLED", count, logged)
	}
}
