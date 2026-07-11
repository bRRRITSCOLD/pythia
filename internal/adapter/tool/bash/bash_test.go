package bash

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
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
	tool := New(t.TempDir(), time.Second, 1<<20)

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
	tool := New(t.TempDir(), time.Second, 1<<20)

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
	tool := New(t.TempDir(), 50*time.Millisecond, 1<<20)

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
	tool := New(dir, time.Second, 1<<20)

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
	tool := New(t.TempDir(), time.Second, 10)

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
	tool := New(t.TempDir(), time.Second, 1<<20)

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
	tool := New(t.TempDir(), time.Second, 1<<20)

	schema := tool.Schema()
	if schema.Name != "bash" {
		t.Errorf("schema.Name = %q, want bash", schema.Name)
	}
	if !strings.Contains(string(schema.Parameters), "command") {
		t.Errorf("schema.Parameters = %s, want it to mention command", schema.Parameters)
	}
}
