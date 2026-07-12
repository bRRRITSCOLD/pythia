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

func TestRun_SimpleCommand_ReExecsAndReturnsOutput(t *testing.T) {
	var out, errb bytes.Buffer
	code, err := Run(context.Background(), Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"},
		"echo spine-ok", &out, &errb)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 || strings.TrimSpace(out.String()) != "spine-ok" {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errb.String())
	}
}

func TestRun_CommandWithMetachars_DeliveredIntactNeverArgv(t *testing.T) {
	var out bytes.Buffer
	// Newline, a subshell, and quotes: if this were argv-interpolated
	// anywhere along the way it would misparse or split into extra tokens.
	code, err := Run(context.Background(), Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"},
		"printf 'a\nb'; echo \" q'q \"", &out, io.Discard)
	if err != nil {
		t.Fatalf("Run: %v", err)
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
	code, err := Run(context.Background(), Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"},
		"ls -1 /proc/self/fd", &out, io.Discard)
	if err != nil {
		t.Fatalf("Run: %v", err)
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
	code, err := Run(context.Background(), Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"},
		"grep NoNewPrivs /proc/self/status", &out, io.Discard)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Fatalf("grep exited %d: %q", code, out.String())
	}
	if !strings.Contains(out.String(), "NoNewPrivs:\t1") {
		t.Fatalf("NO_NEW_PRIVS not set in sandboxed child: %q", out.String())
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
