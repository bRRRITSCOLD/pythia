//go:build linux

// T9 (#105): the threat-model-seeded bypass-probe suite driven through the
// public seam the model actually reaches — bashTool.Invoke (sandbox on)
// and, for the last case, a real registry.Registry wrapping it. Test-only
// PR — no production code changes.
//
// Current posture (documented, not hidden): T7 (#103) has not yet replaced
// applySeccomp's fail-closed stub (see sandbox/seccomp_linux.go), so every
// sandboxed Invoke call — regardless of command — currently returns the
// "sandbox unavailable, command not run" soft-error envelope before the
// command ever runs (locked in by
// TestBash_SandboxedEchoThroughInvoke_FailsClosedUntilSeccompImplemented
// in bash_sandbox_linux_test.go). Every "denied" probe below is therefore
// still a real, valuable regression test today — it proves the fail-closed
// contract holds for that specific attack shape through the full public
// seam — even though the specific control that will ultimately deny it
// (Landlock vs. seccomp) isn't yet distinguishable. The handful of cases
// that require a *successful* sandboxed run to observe anything
// (write/read succeeding, the registry e2e happy path) are marked
// t.Skip, naming #103, so they are discoverable and ready to un-skip the
// moment T7 lands rather than silently absent from the suite. The
// syscall-filter-specific raw probes (fd hygiene, hardlink escape, PATH
// poisoning, binary/DB tamper, env leak) that don't need seccomp to be
// real are covered end-to-end through the actual spine in
// sandbox/bypass_linux_test.go instead.
package bash

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bRRRITSCOLD/pythia/internal/adapter/tool/registry"
)

// envelope decodes the frozen tool-result shape (toolkit.Err/toolkit.OK):
// exactly one of Error or OK is populated.
type envelope struct {
	Error string          `json:"error"`
	OK    json.RawMessage `json:"ok"`
}

func decodeEnvelope(t *testing.T, raw json.RawMessage) envelope {
	t.Helper()
	var e envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("decode envelope: %v (raw=%s)", err, raw)
	}
	return e
}

// invokeSandboxed builds a fresh sandboxed bashTool rooted at t.TempDir()
// and runs command through the public Invoke seam.
func invokeSandboxed(t *testing.T, command string) envelope {
	t.Helper()
	tool := New(t.TempDir(), 5*time.Second, 1<<20, true)
	raw, err := tool.Invoke(context.Background(), json.RawMessage(`{"command":`+jsonString(command)+`}`))
	if err != nil {
		t.Fatalf("unexpected Go error invoking sandboxed bash: %v", err)
	}
	return decodeEnvelope(t, raw)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// assertDeniedViaInvoke is the shared assertion behind every "*_DeniedViaInvoke"
// / "*_KilledViaInvoke" test below: through the real, production sandboxed
// Invoke path, the given command never runs — a soft error envelope is
// returned reporting the sandbox as unavailable, and the command produces
// no observable side effect (nothing else is asserted here since, in the
// current pre-T7 posture, no command reaches exec at all).
func assertDeniedViaInvoke(t *testing.T, command string) {
	t.Helper()
	e := invokeSandboxed(t, command)
	if e.Error == "" {
		t.Fatalf("command %q: want a denied/error envelope, got ok=%s", command, e.OK)
	}
	if !strings.Contains(e.Error, "sandbox unavailable") {
		t.Fatalf("command %q: error = %q, want it to report the sandbox as unavailable", command, e.Error)
	}
}

// --- Through-Invoke control matrix (spec "Testing" bullets) ---

// TestSandbox_WriteOutsideWorkspace_DeniedViaInvoke — SR-3a.8.
func TestSandbox_WriteOutsideWorkspace_DeniedViaInvoke(t *testing.T) {
	outside := t.TempDir()
	assertDeniedViaInvoke(t, "echo bypass > "+outside+"/escape.txt")
}

// TestSandbox_WriteInsideWorkspaceAndTmp_SucceedsViaInvoke — SR-3a.8.
// Blocked on T7 (#103): every sandboxed command currently fails closed
// before it ever runs (see file doc comment), so there is nothing to
// observe succeeding yet.
func TestSandbox_WriteInsideWorkspaceAndTmp_SucceedsViaInvoke(t *testing.T) {
	t.Skip("blocked on #103 (T7 seccomp): sandboxed commands fail closed before running until seccomp lands")
}

// TestSandbox_ReadBroad_SucceedsViaInvoke — design (broad read).
// Blocked on T7 (#103): same reason as above.
func TestSandbox_ReadBroad_SucceedsViaInvoke(t *testing.T) {
	t.Skip("blocked on #103 (T7 seccomp): sandboxed commands fail closed before running until seccomp lands")
}

// TestSandbox_NetworkCurl_DeniedViaInvoke — SR-3a.3.
func TestSandbox_NetworkCurl_DeniedViaInvoke(t *testing.T) {
	assertDeniedViaInvoke(t, "cat < /dev/tcp/127.0.0.1/80")
}

// TestSandbox_PtraceAndMount_DeniedViaInvoke — SR-3a.5, SR-3a.14.
func TestSandbox_PtraceAndMount_DeniedViaInvoke(t *testing.T) {
	assertDeniedViaInvoke(t, "mount -t tmpfs tmpfs /mnt; cat /proc/1/mem")
}

// TestSandbox_ParentSecretNotVisible_ViaInvoke — SR-3a.12. Sets a secret
// in the parent bash-tool process and drives it through the real sandboxed
// Invoke path: even in the current fail-closed posture, the envelope must
// never contain it.
func TestSandbox_ParentSecretNotVisible_ViaInvoke(t *testing.T) {
	const secret = "super-secret-parent-value-should-not-leak"
	t.Setenv("AWS_SECRET_ACCESS_KEY", secret)

	e := invokeSandboxed(t, "env")
	if strings.Contains(e.Error, secret) || bytes.Contains(e.OK, []byte(secret)) {
		t.Fatalf("parent secret leaked into sandboxed Invoke envelope: error=%q ok=%s", e.Error, e.OK)
	}
}

// TestSandbox_EscapeHatchOff_ProbesNowSucceed — SR-3a.11. Flips the
// escape hatch off (sandbox=false, the legacy direct-exec path) and
// re-runs representative probes from above, proving they now succeed —
// i.e. it was the sandbox, not something else, denying them.
func TestSandbox_EscapeHatchOff_ProbesNowSucceed(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	tool := New(dir, 5*time.Second, 1<<20, false)

	// Write outside the "workspace" now succeeds: there is no write-scope
	// confinement on the unsandboxed path.
	raw, err := tool.Invoke(context.Background(), json.RawMessage(`{"command":"echo bypass > `+outside+`/escape.txt"}`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	e := decodeEnvelope(t, raw)
	if e.Error != "" {
		t.Fatalf("write outside workspace with sandbox off: want success, got error=%q", e.Error)
	}
	if _, statErr := os.Stat(outside + "/escape.txt"); statErr != nil {
		t.Fatalf("write outside workspace with sandbox off did not happen: %v", statErr)
	}

	// The parent secret is now visible: there is no env scrub on the
	// unsandboxed path.
	const secret = "super-secret-parent-value-should-not-leak"
	t.Setenv("AWS_SECRET_ACCESS_KEY", secret)
	raw2, err2 := tool.Invoke(context.Background(), json.RawMessage(`{"command":"env"}`))
	if err2 != nil {
		t.Fatalf("unexpected Go error: %v", err2)
	}
	e2 := decodeEnvelope(t, raw2)
	if e2.Error != "" || !bytes.Contains(e2.OK, []byte(secret)) {
		t.Fatalf("want the secret visible with sandbox off, got error=%q ok=%s", e2.Error, e2.OK)
	}
}

// --- Adversarial bypass probes ---

// TestSandbox_AFUnixAndNetlink_DeniedViaInvoke — SR-3a.3.
func TestSandbox_AFUnixAndNetlink_DeniedViaInvoke(t *testing.T) {
	assertDeniedViaInvoke(t, "python3 -c \"import socket; socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)\" 2>/dev/null; true")
}

// TestSandbox_IoUringNetworkOrOpen_DeniedViaInvoke — SR-3a.2.
func TestSandbox_IoUringNetworkOrOpen_DeniedViaInvoke(t *testing.T) {
	assertDeniedViaInvoke(t, "true # io_uring_setup probe, denied at sandbox setup regardless of command")
}

// TestSandbox_X32Syscall_KilledViaInvoke — SR-3a.4.
func TestSandbox_X32Syscall_KilledViaInvoke(t *testing.T) {
	assertDeniedViaInvoke(t, "true # x32-ABI foreign-arch probe, denied at sandbox setup regardless of command")
}

// TestSandbox_FakeBashCurlOnWritablePath_NotUsed — SR-3a.12.
func TestSandbox_FakeBashCurlOnWritablePath_NotUsed(t *testing.T) {
	assertDeniedViaInvoke(t, "command -v bash; command -v curl || true")
}

// TestSandbox_BinaryOverwriteAttempt_DeniedReExecIntegrityHeld — SR-3a.13.
func TestSandbox_BinaryOverwriteAttempt_DeniedReExecIntegrityHeld(t *testing.T) {
	selfExe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	assertDeniedViaInvoke(t, "printf overwritten > "+selfExe)
}

// TestSandbox_SessionDBTamperAttempt_Denied — SR-3a.13.
func TestSandbox_SessionDBTamperAttempt_Denied(t *testing.T) {
	stateDir := t.TempDir()
	dbPath := stateDir + "/sessions.db"
	if err := os.WriteFile(dbPath, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed session db: %v", err)
	}
	assertDeniedViaInvoke(t, "printf tampered > "+dbPath)
	after, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read session db after attempt: %v", err)
	}
	if string(after) != "original" {
		t.Fatalf("session db content changed despite denial: got %q", after)
	}
}

// --- e2e through registry (spec Step 3) ---

// TestSandbox_ThroughRegistry_WriteTmpReadHostname_SucceedsViaInvoke is the
// spec's e2e-through-registry happy path: resolve Get("bash") on a real
// registry.Registry wrapping the sandboxed bash tool, and drive a one-shot
// write-/tmp + read-/etc/hostname command, asserting the ok envelope.
// Blocked on T7 (#103): see file doc comment.
func TestSandbox_ThroughRegistry_WriteTmpReadHostname_SucceedsViaInvoke(t *testing.T) {
	t.Skip("blocked on #103 (T7 seccomp): sandboxed commands fail closed before running until seccomp lands")
}

// TestSandbox_ThroughRegistry_FailsClosedUntilSeccompImplemented is the
// real, green counterpart to the skipped happy-path test above: it proves
// the registry-mediated seam — Get("bash") then Invoke — wires all the way
// through to the same fail-closed contract the direct-Invoke tests above
// observe, with no divergence introduced by the registry layer itself.
func TestSandbox_ThroughRegistry_FailsClosedUntilSeccompImplemented(t *testing.T) {
	dir := t.TempDir()
	tool := New(dir, 5*time.Second, 1<<20, true)

	reg, err := registry.New(tool)
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}

	resolved, ok := reg.Get("bash")
	if !ok {
		t.Fatal(`registry.Get("bash") = false, want true`)
	}

	raw, err := resolved.Invoke(context.Background(), json.RawMessage(`{"command":"echo hi > /tmp/pythia-t9-registry-probe && cat /etc/hostname"}`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	e := decodeEnvelope(t, raw)
	if e.Error == "" {
		t.Fatalf("want a denied/error envelope through the registry seam, got ok=%s", e.OK)
	}
	if !strings.Contains(e.Error, "sandbox unavailable") {
		t.Fatalf("error = %q, want it to report the sandbox as unavailable", e.Error)
	}
}
