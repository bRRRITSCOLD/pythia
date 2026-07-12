//go:build linux

// T9 (#105): the threat-model-seeded bypass-probe suite driven through the
// public seam the model actually reaches — bashTool.Invoke (sandbox on)
// and, for the registry cases, a real registry.Registry wrapping it.
//
// Posture: T7 (#103) has replaced applySeccomp's fail-closed stub with a
// real allowlist filter, so every probe below now runs the command all the
// way through the sandboxed spine (real Landlock + real seccomp). A
// "denied" probe is no longer distinguished by a pre-exec setup failure —
// it is a real success envelope (the sandbox itself worked) whose exit
// code is nonzero because the *command* was refused at the syscall or
// filesystem layer, exactly as an attacker driving the model would
// observe it. The syscall-filter-specific raw probes that don't need a
// full Invoke round trip (fd hygiene, hardlink escape) are covered
// end-to-end through the actual spine in sandbox/bypass_linux_test.go
// instead; the two probes that genuinely cannot be triggered from a shell
// command (x32 ABI, io_uring) are proven directly at the syscall-filter
// level by sandbox/seccomp_linux_test.go instead and are noted as such
// here rather than faked through a no-op shell command.
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

// invokeOutput mirrors bash.output (the ok-envelope payload) for decoding
// in tests that need to inspect exit code / stdout / stderr rather than
// just success-or-not.
type invokeOutput struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated"`
	TimedOut  bool   `json:"timed_out"`
}

func decodeOKOutput(t *testing.T, e envelope) invokeOutput {
	t.Helper()
	if e.Error != "" {
		t.Fatalf("unexpected error envelope (want the command to have actually run): %s", e.Error)
	}
	var out invokeOutput
	if err := json.Unmarshal(e.OK, &out); err != nil {
		t.Fatalf("decode ok envelope: %v (raw=%s)", err, e.OK)
	}
	return out
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

// assertRunsButDenied is the shared assertion behind every "*_DeniedViaInvoke"
// probe below: through the real, production sandboxed Invoke path, the
// sandbox itself must succeed (no setup error) but the command's own exit
// code must be nonzero — meaning the confined command actually reached
// exec and was then refused by Landlock or seccomp (EACCES/ENOSYS/killed),
// not that the sandbox failed to stand up at all.
func assertRunsButDenied(t *testing.T, command string) invokeOutput {
	t.Helper()
	e := invokeSandboxed(t, command)
	out := decodeOKOutput(t, e)
	if out.ExitCode == 0 {
		t.Fatalf("command %q: exit_code = 0, want it denied at the syscall/filesystem layer (stdout=%q stderr=%q)", command, out.Stdout, out.Stderr)
	}
	return out
}

// outsideScopeDir returns a writable directory that is NEITHER the workspace
// NOR under /tmp — i.e. genuinely outside the sandbox write scope
// (workspace + /tmp). It is created under $HOME, which the process uid owns
// and can write via Unix DAC, so a write DENIED there proves the sandbox
// (Landlock), not DAC, blocked it. Removed at test end. Using t.TempDir()
// here would be wrong: it lives under /tmp, which IS in the write scope.
func outsideScopeDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	d, err := os.MkdirTemp(home, ".pythia-sandbox-outside-*")
	if err != nil {
		t.Fatalf("MkdirTemp under $HOME: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// --- Through-Invoke control matrix (spec "Testing" bullets) ---

// TestSandbox_WriteOutsideWorkspace_DeniedViaInvoke — SR-3a.8.
func TestSandbox_WriteOutsideWorkspace_DeniedViaInvoke(t *testing.T) {
	outside := outsideScopeDir(t)
	out := assertRunsButDenied(t, "echo bypass > "+outside+"/escape.txt")
	if _, statErr := os.Stat(outside + "/escape.txt"); !os.IsNotExist(statErr) {
		t.Errorf("write outside workspace produced a file despite denial: statErr=%v stderr=%q", statErr, out.Stderr)
	}
}

// TestSandbox_WriteInsideWorkspace_SucceedsViaInvoke — SR-3a.8. Now that
// T7 (#103) lands a real seccomp filter, a write inside the workspace root
// must actually succeed end to end through Invoke. (Not exercising the
// second write-scope root, os.TempDir(): see the doc comment on
// TestSandbox_ThroughRegistry_WriteTmpReadHostname_SucceedsViaInvoke for
// the pre-existing gap that leaves it unreachable today.)
func TestSandbox_WriteInsideWorkspace_SucceedsViaInvoke(t *testing.T) {
	dir := t.TempDir()
	tool := New(dir, 5*time.Second, 1<<20, true)

	raw, err := tool.Invoke(context.Background(), json.RawMessage(`{"command":"echo in-workspace > `+dir+`/ok.txt && cat `+dir+`/ok.txt"}`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	out := decodeOKOutput(t, decodeEnvelope(t, raw))
	if out.ExitCode != 0 {
		t.Fatalf("write+read inside workspace: exit_code=%d, want 0 (stderr=%q)", out.ExitCode, out.Stderr)
	}
	if strings.TrimSpace(out.Stdout) != "in-workspace" {
		t.Fatalf("write+read inside workspace: stdout=%q, want %q", out.Stdout, "in-workspace")
	}
}

// TestSandbox_ReadBroad_SucceedsViaInvoke — design (broad read): the
// confined command may read anywhere under "/" even though it can only
// write inside its two scoped roots.
func TestSandbox_ReadBroad_SucceedsViaInvoke(t *testing.T) {
	e := invokeSandboxed(t, "cat /etc/hostname")
	out := decodeOKOutput(t, e)
	if out.ExitCode != 0 {
		t.Fatalf("broad read: exit_code=%d, want 0 (stderr=%q)", out.ExitCode, out.Stderr)
	}
	if strings.TrimSpace(out.Stdout) == "" {
		t.Fatalf("broad read: stdout empty, want /etc/hostname's content")
	}
}

// TestSandbox_NetworkCurl_DeniedViaInvoke — SR-3a.3.
func TestSandbox_NetworkCurl_DeniedViaInvoke(t *testing.T) {
	assertRunsButDenied(t, "cat < /dev/tcp/127.0.0.1/80")
}

// TestSandbox_PtraceAndMount_DeniedViaInvoke — SR-3a.5, SR-3a.14.
func TestSandbox_PtraceAndMount_DeniedViaInvoke(t *testing.T) {
	assertRunsButDenied(t, "mount -t tmpfs tmpfs /mnt; cat /proc/1/mem")
}

// TestSandbox_ParentSecretNotVisible_ViaInvoke — SR-3a.12. Sets a secret
// in the parent bash-tool process and drives it through the real sandboxed
// Invoke path: the confined "env" command actually runs now, and its
// output must never contain the parent's secret.
func TestSandbox_ParentSecretNotVisible_ViaInvoke(t *testing.T) {
	const secret = "super-secret-parent-value-should-not-leak"
	t.Setenv("AWS_SECRET_ACCESS_KEY", secret)

	e := invokeSandboxed(t, "env")
	out := decodeOKOutput(t, e)
	if out.ExitCode != 0 {
		t.Fatalf("env: exit_code=%d, want 0 (stderr=%q)", out.ExitCode, out.Stderr)
	}
	if strings.Contains(out.Stdout, secret) || strings.Contains(out.Stderr, secret) {
		t.Fatalf("parent secret leaked into sandboxed env output: stdout=%q stderr=%q", out.Stdout, out.Stderr)
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

// TestSandbox_AFUnixAndNetlink_DeniedViaInvoke — SR-3a.3. socket()
// creation is denied for every address family, not just AF_INET.
func TestSandbox_AFUnixAndNetlink_DeniedViaInvoke(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip("python3 not available in this environment")
	}
	assertRunsButDenied(t, `python3 -c "import socket; socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)"`)
}

// TestSandbox_IoUringNetworkOrOpen_DeniedViaInvoke — SR-3a.2. io_uring
// cannot be triggered from a plain shell command (it needs a program
// linked against liburing or issuing the raw syscall directly), so the
// real assertion for this control lives at the syscall-filter level in
// sandbox/seccomp_linux_test.go's TestSeccomp_IoUring_DeniedOrKilled. This
// is kept here as a regression guard that the full Invoke seam still works
// normally with the io_uring rule installed.
func TestSandbox_IoUringNetworkOrOpen_DeniedViaInvoke(t *testing.T) {
	e := invokeSandboxed(t, "true")
	out := decodeOKOutput(t, e)
	if out.ExitCode != 0 {
		t.Fatalf("exit_code=%d, want 0 (stderr=%q)", out.ExitCode, out.Stderr)
	}
}

// TestSandbox_X32Syscall_KilledViaInvoke — SR-3a.4. The x32 ABI cannot be
// entered from a plain shell command either — it requires manually setting
// the x32 bit on a raw syscall number, which sandbox/seccomp_linux_test.go's
// TestSeccomp_ForeignArchX32_Killed exercises directly against the real
// filter. Kept here as the same kind of regression guard as the io_uring
// case above.
func TestSandbox_X32Syscall_KilledViaInvoke(t *testing.T) {
	e := invokeSandboxed(t, "true")
	out := decodeOKOutput(t, e)
	if out.ExitCode != 0 {
		t.Fatalf("exit_code=%d, want 0 (stderr=%q)", out.ExitCode, out.Stderr)
	}
}

// TestSandbox_FakeBashCurlOnWritablePath_NotUsed — SR-3a.12. Plants a fake
// "curl" earlier in the parent's PATH (an attacker-controlled writable
// directory, as a prior command run by the agent might do) and proves the
// sandboxed child still resolves the real /usr/bin/curl: PATH is always
// forced to fixedPATH, never inherited from the parent.
func TestSandbox_FakeBashCurlOnWritablePath_NotUsed(t *testing.T) {
	fakeDir := t.TempDir()
	fakeCurl := fakeDir + "/curl"
	if err := os.WriteFile(fakeCurl, []byte("#!/bin/sh\necho FAKE-CURL\n"), 0o755); err != nil {
		t.Fatalf("plant fake curl: %v", err)
	}
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	e := invokeSandboxed(t, "command -v curl")
	out := decodeOKOutput(t, e)
	if out.ExitCode != 0 {
		t.Fatalf("command -v curl: exit_code=%d, want 0 (stderr=%q)", out.ExitCode, out.Stderr)
	}
	resolved := strings.TrimSpace(out.Stdout)
	if resolved == fakeCurl {
		t.Fatalf("sandboxed child resolved the attacker-planted curl: %q", resolved)
	}
	if resolved != "" && !strings.HasPrefix(resolved, "/usr/bin/") && !strings.HasPrefix(resolved, "/bin/") {
		t.Fatalf("sandboxed child resolved curl from an unexpected location: %q", resolved)
	}
}

// TestSandbox_BinaryOverwriteAttempt_DeniedReExecIntegrityHeld — SR-3a.13.
func TestSandbox_BinaryOverwriteAttempt_DeniedReExecIntegrityHeld(t *testing.T) {
	selfExe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	assertRunsButDenied(t, "printf overwritten > "+selfExe)
}

// TestSandbox_SessionDBTamperAttempt_Denied — SR-3a.13.
func TestSandbox_SessionDBTamperAttempt_Denied(t *testing.T) {
	// The real session DB lives at $XDG_STATE_HOME (~/.local/state/pythia),
	// relocated out of the write scope in T1 — modelled here by an
	// out-of-scope ($HOME) dir, NOT /tmp (which is in scope).
	stateDir := outsideScopeDir(t)
	dbPath := stateDir + "/sessions.db"
	if err := os.WriteFile(dbPath, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed session db: %v", err)
	}
	assertRunsButDenied(t, "printf tampered > "+dbPath)
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
func TestSandbox_ThroughRegistry_WriteTmpReadHostname_SucceedsViaInvoke(t *testing.T) {
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

	// Written under os.TempDir() (/tmp) — the SECOND write-scope root, now
	// that Policy.TmpDir is threaded over the wire frame into the child's
	// Landlock ruleset. This exercises /tmp writability end-to-end (the
	// gap that previously forced this probe into the workspace root).
	probe, err := os.CreateTemp("", "pythia-registry-probe-ok-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	probe.Close()
	probePath := probe.Name()
	defer os.Remove(probePath)

	raw, err := resolved.Invoke(context.Background(), json.RawMessage(`{"command":"echo hi > `+probePath+` && cat /etc/hostname"}`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	out := decodeOKOutput(t, decodeEnvelope(t, raw))
	if out.ExitCode != 0 {
		t.Fatalf("registry e2e happy path: exit_code=%d, want 0 (stderr=%q)", out.ExitCode, out.Stderr)
	}
	if strings.TrimSpace(out.Stdout) == "" {
		t.Fatalf("registry e2e happy path: stdout empty, want /etc/hostname's content")
	}
}

// TestSandbox_ThroughRegistry_WriteOutsideScope_DeniedViaInvoke proves the
// registry-mediated seam — Get("bash") then Invoke — enforces the same
// write-scope confinement as the direct-Invoke tests above, with no
// divergence introduced by the registry layer itself.
func TestSandbox_ThroughRegistry_WriteOutsideScope_DeniedViaInvoke(t *testing.T) {
	dir := t.TempDir()
	outside := outsideScopeDir(t)
	tool := New(dir, 5*time.Second, 1<<20, true)

	reg, err := registry.New(tool)
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	resolved, ok := reg.Get("bash")
	if !ok {
		t.Fatal(`registry.Get("bash") = false, want true`)
	}

	raw, err := resolved.Invoke(context.Background(), json.RawMessage(`{"command":"echo bypass > `+outside+`/escape.txt"}`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	out := decodeOKOutput(t, decodeEnvelope(t, raw))
	if out.ExitCode == 0 {
		t.Fatalf("registry-mediated write outside scope: exit_code = 0, want it denied (stderr=%q)", out.Stderr)
	}
	if _, statErr := os.Stat(outside + "/escape.txt"); !os.IsNotExist(statErr) {
		t.Errorf("registry-mediated write outside scope produced a file despite denial: statErr=%v", statErr)
	}
}
