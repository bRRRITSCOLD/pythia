//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// forceFailSubcommand is a test-only reserved marker, distinct from the
// real ChildSubcommand: TestMain routes it to forceFailChild instead of the
// real RunChild, letting TestRun_ChildSetupFails_FailsClosedCommandNotRun
// exercise the parent's fail-closed arm (SR-3a.10) without needing to
// contrive a real apply-step failure (the no-op T5 stubs never fail).
const forceFailSubcommand = "__bash-sandbox-test-force-fail"

// noSeccompSubcommand is a second test-only reserved marker: TestMain
// routes it to noSeccompChild, which runs the real frozen apply sequence
// (thread lock, frame read, chdir, NO_NEW_PRIVS, env scrub, real
// applyLandlock, fd hygiene, exec) but substitutes a no-op for applySeccomp.
// This lets the spine-mechanics tests below keep exercising the rest of the
// real sequence while T7 (#103) is still a fail-closed stub — see
// applySeccomp's doc comment. The real production path (ChildSubcommand)
// is exercised separately by
// TestRun_ProductionPath_FailsClosedUntilSeccompImplemented.
const noSeccompSubcommand = "__bash-sandbox-test-no-seccomp"

// TestMain lets this test binary act as its own re-exec target: Run always
// re-execs the current binary via /proc/self/exe, which — inside `go test`
// — is this very test binary, not cmd/pythia. Intercepting the reserved
// subcommands here before the normal test run mirrors exactly what
// cmd/pythia/main.go's one-line hook does in production (mirrors the
// standard library's own recursive-self-exec test pattern, e.g.
// os/exec_test.go's TestHelperProcess).
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case ChildSubcommand:
			os.Exit(RunChild())
		case forceFailSubcommand:
			os.Exit(forceFailChild())
		case noSeccompSubcommand:
			os.Exit(runChildWithApply(applyLandlock, func() error { return nil }))
		}
	}
	os.Exit(m.Run())
}

// forceFailChild simulates a child setup failure without touching the real
// apply sequence: it writes a synthetic error to the reserved error-pipe fd
// and exits non-zero, never reading the frame and never exec'ing bash — so
// whatever command the parent asked for is provably never run.
func forceFailChild() int {
	errFile := os.NewFile(uintptr(errFD), "sandbox-err")
	fmt.Fprintln(errFile, "forced failure for TestRun_ChildSetupFails_FailsClosedCommandNotRun")
	_ = errFile.Close()
	return 1
}

// runSpine is execSandboxed pinned to noSeccompSubcommand: the spine
// mechanics tests below want to exercise the real sequence end to end
// without being blocked by T7's fail-closed seccomp stub.
func runSpine(ctx context.Context, p Policy, command string, stdout, stderr io.Writer) (int, error) {
	return execSandboxed(ctx, p, command, stdout, stderr, noSeccompSubcommand)
}

func TestRun_SimpleCommand_ReExecsAndReturnsOutput(t *testing.T) {
	var out, errb bytes.Buffer
	code, err := runSpine(context.Background(), Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"},
		"echo spine-ok", &out, &errb)
	if err != nil {
		t.Fatalf("runSpine: %v", err)
	}
	if code != 0 || strings.TrimSpace(out.String()) != "spine-ok" {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errb.String())
	}
}

func TestRun_CommandWithMetachars_DeliveredIntactNeverArgv(t *testing.T) {
	var out bytes.Buffer
	// Newline, a subshell, and quotes: if this were argv-interpolated
	// anywhere along the way it would misparse or split into extra tokens.
	code, err := runSpine(context.Background(), Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"},
		"printf 'a\nb'; echo \" q'q \"", &out, io.Discard)
	if err != nil {
		t.Fatalf("runSpine: %v", err)
	}
	if code != 0 {
		t.Fatalf("command exited %d: %q", code, out.String())
	}
	if !strings.Contains(out.String(), "a\nb") {
		t.Fatalf("command bytes garbled: %q", out.String())
	}
	if !strings.Contains(out.String(), " q'q ") {
		t.Fatalf("command bytes garbled: %q", out.String())
	}
}

func TestRun_FdHygiene_OnlyStdioReachesChild(t *testing.T) {
	// The parent deliberately holds an extra open fd (a temp file) across
	// Run: Go opens it O_CLOEXEC by default, so this mainly guards against
	// our own sandbox plumbing accidentally widening what it hands the
	// child beyond the two ExtraFiles it means to pass.
	f, err := os.CreateTemp(t.TempDir(), "leak")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

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
	// one fd >= 3 (ls's own dirfd) is expected noise; more than one means a
	// real leak — in particular frameFD/errFD (3 and 4) both showing up.
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
		t.Errorf("more than one fd >= 3 reached the sandbox child (SR-3a.7): %v (raw=%q)", leaked, out.String())
	}
}

func TestRun_NoNewPrivs_SetuidBinaryGainsNothing(t *testing.T) {
	var out bytes.Buffer
	code, err := runSpine(context.Background(), Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"},
		"grep NoNewPrivs /proc/self/status", &out, io.Discard)
	if err != nil {
		t.Fatalf("runSpine: %v", err)
	}
	if code != 0 {
		t.Fatalf("grep exited %d: %q", code, out.String())
	}
	if !strings.Contains(out.String(), "NoNewPrivs:\t1") {
		t.Fatalf("NO_NEW_PRIVS not set in sandboxed child: %q", out.String())
	}
}

// TestRun_WorkDir_ChildRunsInWorkspaceRoot locks in SR-3b for the sandboxed
// path: the confined command must observe Policy.WorkspaceRoot as its
// working directory, the same contract the legacy direct-exec path gets
// via cmd.Dir. Regression test for the missing os.Chdir in runChild.
func TestRun_WorkDir_ChildRunsInWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", root, err)
	}

	var out bytes.Buffer
	code, err := runSpine(context.Background(), Policy{WorkspaceRoot: root, TmpDir: "/tmp"},
		"pwd", &out, io.Discard)
	if err != nil {
		t.Fatalf("runSpine: %v", err)
	}
	if code != 0 {
		t.Fatalf("pwd exited %d: %q", code, out.String())
	}
	if got := strings.TrimSpace(out.String()); got != resolvedRoot {
		t.Errorf("pwd = %q, want %q (sandboxed child did not chdir into WorkspaceRoot)", got, resolvedRoot)
	}
}

// TestRun_ProductionPath_RunsWithRealSeccomp exercises the real production
// path (ChildSubcommand, i.e. what bashTool.Invoke drives) now that T7
// (#103) has replaced applySeccomp's fail-closed stub with a real filter:
// a benign command must run to completion through the full frozen
// sequence — real Landlock and real seccomp both installed, unlike the
// noSeccompSubcommand test double the rest of this file's spine-mechanics
// tests use.
func TestRun_ProductionPath_RunsWithRealSeccomp(t *testing.T) {
	var out bytes.Buffer
	code, err := Run(context.Background(), Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"},
		"echo production-ok", &out, io.Discard)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if got := strings.TrimSpace(out.String()); got != "production-ok" {
		t.Errorf("stdout = %q, want %q", got, "production-ok")
	}
}

func TestRun_ChildSetupFails_FailsClosedCommandNotRun(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "should-not-exist")

	var out bytes.Buffer
	code, err := execSandboxed(context.Background(),
		Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"},
		"touch "+sentinel, &out, io.Discard, forceFailSubcommand)

	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("execSandboxed err = %v, want ErrUnsupported", err)
	}
	if code != -1 {
		t.Errorf("execSandboxed code = %d, want -1", code)
	}
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Errorf("command side effect happened despite fail-closed setup failure: statErr=%v", statErr)
	}
}
