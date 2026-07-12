//go:build linux

// Package sandbox bypass probes (T9, #105): the threat-model-seeded
// adversarial probes that need the real spine + real Landlock actually
// running a confined child — as opposed to bypass_linux_test.go's sibling
// spine_linux_test.go, which covers general spine mechanics, and
// landlock_linux_test.go, which drives applyLandlock directly via an
// isolated helper process. These probes reuse spine_linux_test.go's
// runSpine (execSandboxed pinned to noSeccompSubcommand): they exercise
// the real frozen apply sequence — thread lock, frame read, chdir,
// NO_NEW_PRIVS, env scrub, real Landlock, fd hygiene, exec — without being
// gated on T7 (#103)'s still-stubbed seccomp layer. The syscall-filter
// probes T9 also enumerates (AF_UNIX/netlink, io_uring, x32, ptrace/mount)
// are exercised through the public bashTool.Invoke seam in
// sandbox_integration_linux_test.go instead, since they need the real
// production path — see that file's doc comment for the current
// fail-closed-until-T7 posture.
package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestSandbox_InheritedFdProbe_NotPresentInChild (SR-3a.7) is an
// adversarial variant of spine_linux_test.go's
// TestRun_FdHygiene_OnlyStdioReachesChild: rather than a single incidental
// open file, it opens several extra CLOEXEC fds first — shifting which
// physical fd numbers frameFD/errFD land on — to prove the spine's fixed
// ExtraFiles allowlist keeps the child's visible fd set to exactly
// {0,1,2,frameFD,errFD}'s dup'd successors regardless of what else the
// parent process happens to have open, not merely by accident of a
// particular fd layout.
//
// A raw fd opened without O_CLOEXEC at all (e.g. via unix.Open) is
// deliberately not probed here: on Unix, any such fd is inherited by any
// child a process execs, sandboxed or not — that is the general contract
// of fork+exec, not something the sandbox's own hygiene can or should
// paper over. Every fd pythia itself opens goes through Go's os package,
// which sets O_CLOEXEC by default (verified by TestRun_FdHygiene_OnlyStdioReachesChild
// and this test); guarding against a hypothetical future raw non-CLOEXEC
// open elsewhere in the codebase is tracked separately as a defense-in-depth
// follow-up, not a T9 regression test (no reproducible bug exists today).
func TestSandbox_InheritedFdProbe_NotPresentInChild(t *testing.T) {
	dir := t.TempDir()
	var extras []*os.File
	for i := 0; i < 5; i++ {
		f, err := os.CreateTemp(dir, "extra")
		if err != nil {
			t.Fatalf("open extra fd %d: %v", i, err)
		}
		extras = append(extras, f)
	}
	defer func() {
		for _, f := range extras {
			_ = f.Close()
		}
	}()

	var out bytes.Buffer
	code, err := runSpine(context.Background(), Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"},
		"ls -1 /proc/self/fd", &out, io.Discard)
	if err != nil {
		t.Fatalf("runSpine: %v", err)
	}
	if code != 0 {
		t.Fatalf("ls exited %d: %s", code, out.String())
	}

	// ls itself transiently opens the directory it is listing, so exactly
	// one fd >= 3 is expected noise; more than one means something beyond
	// the sandbox's own two ExtraFiles reached the child.
	var leaked []int
	for _, field := range strings.Fields(out.String()) {
		fd, convErr := strconv.Atoi(field)
		if convErr != nil {
			continue
		}
		if fd >= 3 {
			leaked = append(leaked, fd)
		}
	}
	if len(leaked) > 1 {
		t.Errorf("more than one fd >= 3 reached the sandboxed child despite %d extra CLOEXEC fds open in the parent (SR-3a.7): %v (raw=%q)", len(extras), leaked, out.String())
	}
}

// TestSandbox_FakeBashCurlOnWritablePath_NotUsed (SR-3a.12) plants fake
// "bash" and "curl" executables in a writable directory and poisons the
// parent process's own PATH to put that directory first — simulating a
// prior command having planted an injector on a writable path (threat
// model §2.5). It proves the confined child neither resolves the shell
// via PATH (bashPath is a hardcoded const, never looked up) nor lets the
// scrubbed environment's forced fixedPATH pick up the planted directory.
func TestSandbox_FakeBashCurlOnWritablePath_NotUsed(t *testing.T) {
	trap := t.TempDir()
	for _, name := range []string{"bash", "curl"} {
		fake := filepath.Join(trap, name)
		script := "#!/bin/sh\necho FAKE-" + strings.ToUpper(name) + "\n"
		if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
			t.Fatalf("plant fake %s: %v", name, err)
		}
	}

	t.Setenv("PATH", trap+":"+os.Getenv("PATH"))

	var out bytes.Buffer
	code, err := runSpine(context.Background(), Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"},
		"command -v bash; command -v curl || true", &out, io.Discard)
	if err != nil {
		t.Fatalf("runSpine: %v", err)
	}
	if code != 0 {
		t.Fatalf("command -v exited %d: %s", code, out.String())
	}

	if strings.Contains(out.String(), trap) {
		t.Errorf("confined child resolved a binary from the poisoned/planted PATH directory: %q", out.String())
	}
	if !strings.Contains(out.String(), bashPath) {
		t.Errorf("confined child did not resolve the real %s: %q", bashPath, out.String())
	}
}

// TestSandbox_HardlinkOutOfScopeThenWrite_DeniedViaInvoke (SR-3a.8)
// exercises the classic hardlink write-scope escape end-to-end through the
// real spine (rather than landlock_linux_test.go's isolated
// applyLandlock-only helper): link a file from outside the write scope
// into the workspace, then attempt to write through the new name.
func TestSandbox_HardlinkOutOfScopeThenWrite_DeniedViaInvoke(t *testing.T) {
	dirs := mkSubdirs(t, "ws", "tmp", "outside")
	ws, tmp, outside := dirs[0], dirs[1], dirs[2]

	src := filepath.Join(outside, "source.txt")
	if err := os.WriteFile(src, []byte("source-content"), 0o644); err != nil {
		t.Fatalf("seed source file: %v", err)
	}
	dst := filepath.Join(ws, "linked.txt")

	var out bytes.Buffer
	code, err := runSpine(context.Background(), Policy{WorkspaceRoot: ws, TmpDir: tmp},
		fmt.Sprintf("ln %q %q && echo bypass > %q", src, dst, dst), &out, io.Discard)
	if err != nil {
		t.Fatalf("runSpine: %v", err)
	}
	if code == 0 {
		t.Fatalf("hardlink-then-write out of scope unexpectedly succeeded: %s", out.String())
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Errorf("hardlink-then-write out of scope produced a file despite denial: statErr=%v", statErr)
	}
}

// TestSandbox_BinaryOverwriteAttempt_DeniedReExecIntegrityHeld (SR-3a.13)
// treats the running test binary as a stand-in for the pythia binary the
// re-exec spine always launches via /proc/self/exe: it lives outside the
// confined write scope, exactly as the real installed binary does relative
// to a workspace root. Proves an attempt to overwrite it is denied and
// that the spine still works afterward (integrity held — no partial write
// corrupted anything a subsequent invocation depends on).
func TestSandbox_BinaryOverwriteAttempt_DeniedReExecIntegrityHeld(t *testing.T) {
	selfExe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	resolvedSelf, err := filepath.EvalSymlinks(selfExe)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", selfExe, err)
	}

	before, err := os.ReadFile(resolvedSelf)
	if err != nil {
		t.Fatalf("read self binary before attempt: %v", err)
	}

	var out bytes.Buffer
	code, err := runSpine(context.Background(), Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"},
		fmt.Sprintf("printf overwritten > %q", resolvedSelf), &out, io.Discard)
	if err != nil {
		t.Fatalf("runSpine: %v", err)
	}
	if code == 0 {
		t.Fatalf("overwrite of the running binary unexpectedly succeeded: %s", out.String())
	}

	after, err := os.ReadFile(resolvedSelf)
	if err != nil {
		t.Fatalf("read self binary after attempt: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("self binary content changed despite denial (re-exec integrity did not hold)")
	}

	// Re-exec integrity held: the spine still works for a fresh command.
	var out2 bytes.Buffer
	code2, err2 := runSpine(context.Background(), Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"},
		"echo still-ok", &out2, io.Discard)
	if err2 != nil {
		t.Fatalf("runSpine after overwrite attempt: %v", err2)
	}
	if code2 != 0 || strings.TrimSpace(out2.String()) != "still-ok" {
		t.Fatalf("spine unusable after denied overwrite attempt: code=%d out=%q", code2, out2.String())
	}
}

// TestSandbox_SessionDBTamperAttempt_Denied (SR-3a.13) simulates an
// attempt to tamper with pythia's own session-store file, which — like the
// binary — lives outside the confined command's write scope (workspace
// root + tmp only). Proves the attempt is denied and the file is left
// byte-for-byte untouched.
func TestSandbox_SessionDBTamperAttempt_Denied(t *testing.T) {
	stateDir := t.TempDir()
	dbPath := filepath.Join(stateDir, "sessions.db")
	original := []byte("sqlite-format-3-placeholder-session-data")
	if err := os.WriteFile(dbPath, original, 0o644); err != nil {
		t.Fatalf("seed session db: %v", err)
	}

	var out bytes.Buffer
	code, err := runSpine(context.Background(), Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"},
		fmt.Sprintf("printf tampered > %q", dbPath), &out, io.Discard)
	if err != nil {
		t.Fatalf("runSpine: %v", err)
	}
	if code == 0 {
		t.Fatalf("session db tamper attempt unexpectedly succeeded: %s", out.String())
	}

	after, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read session db after attempt: %v", err)
	}
	if !bytes.Equal(original, after) {
		t.Fatalf("session db content changed despite denial: got %q, want %q", after, original)
	}
}

// TestSandbox_ParentSecretNotVisible_ViaInvoke (SR-3a.12) proves the env
// scrub actually runs when driven through the real spine: a secret set in
// the parent's own environment must not reach the confined child's `env`
// output. (Named "ViaInvoke" to match the spec's Testing bullet; it
// exercises the same env-scrub control the public bashTool.Invoke seam
// relies on — see sandbox_integration_linux_test.go for the
// production-seam counterpart.)
func TestSandbox_ParentSecretNotVisible_ViaInvoke(t *testing.T) {
	const secret = "super-secret-parent-value-should-not-leak"
	t.Setenv("AWS_SECRET_ACCESS_KEY", secret)

	var out bytes.Buffer
	code, err := runSpine(context.Background(), Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"},
		"env", &out, io.Discard)
	if err != nil {
		t.Fatalf("runSpine: %v", err)
	}
	if code != 0 {
		t.Fatalf("env exited %d: %s", code, out.String())
	}
	if strings.Contains(out.String(), secret) {
		t.Errorf("parent secret leaked into confined child's env: %q", out.String())
	}
}
