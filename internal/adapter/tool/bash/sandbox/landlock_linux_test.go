//go:build linux

package sandbox

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	seccomp "github.com/elastic/go-seccomp-bpf"
)

// Exit codes reported by the landlock helper subprocess (see
// landlockHelperMain and TestLandlockHelperProcess below). Shared between
// the helper and its assertions here so both sides speak the same
// vocabulary.
const (
	exitOK         = 0  // requested filesystem operation succeeded
	exitUsageErr   = 2  // bad helper invocation (test bug, not a landlock outcome)
	exitUnexpected = 3  // op failed with something other than a permission error
	exitApplyErr   = 10 // applyLandlock itself returned a non-nil error
	exitDenied     = 20 // applyLandlock succeeded, then the op was denied (EACCES/EPERM)
)

// landlockHelperEnv gates TestLandlockHelperProcess: it is a plain no-op
// under a normal `go test` run and only does its real work when re-invoked
// as a subprocess with this variable set (mirrors the standard library's
// own TestHelperProcess idiom, e.g. os/exec_test.go).
//
// A subprocess is required, not optional: applyLandlock's underlying
// landlock.Config.RestrictPaths call — on success — permanently confines
// the calling process's filesystem access for the rest of its lifetime.
// Applying it in-process here would leak that confinement into every test
// that runs afterward in the same `go test` binary.
const landlockHelperEnv = "PYTHIA_LANDLOCK_TEST_HELPER"

// runLandlockHelper re-execs this test binary, selecting only
// TestLandlockHelperProcess via -test.run and handing it action + args
// after a "--" terminator. It returns the subprocess's exit code plus its
// captured stdout/stderr for assertions.
func runLandlockHelper(t *testing.T, action string, args ...string) (exitCode int, stdout, stderr string) {
	t.Helper()

	cmdArgs := append([]string{"-test.run=^TestLandlockHelperProcess$", "--", action}, args...)
	cmd := exec.Command(os.Args[0], cmdArgs...)
	cmd.Env = append(os.Environ(), landlockHelperEnv+"=1")

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	if err == nil {
		return exitOK, outBuf.String(), errBuf.String()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), outBuf.String(), errBuf.String()
	}
	t.Fatalf("run landlock helper: %v", err)
	return -1, "", ""
}

// TestLandlockHelperProcess is not a real test: under a normal `go test`
// run (landlockHelperEnv unset) it returns immediately and reports nothing.
// Re-invoked by runLandlockHelper with the env var set, it becomes this
// binary's re-exec target for exercising applyLandlock in an isolated
// process — see landlockHelperEnv.
func TestLandlockHelperProcess(t *testing.T) {
	if os.Getenv(landlockHelperEnv) != "1" {
		return
	}
	os.Exit(landlockHelperMain(os.Args))
}

// landlockHelperMain dispatches the action named after the "--" terminator
// in args (see runLandlockHelper) and returns the process exit code.
func landlockHelperMain(args []string) int {
	action := trimAfterDoubleDash(args)
	if len(action) < 1 {
		fmt.Fprintln(os.Stderr, "landlock helper: missing action")
		return exitUsageErr
	}

	switch action[0] {
	case "write":
		if len(action) != 4 {
			return usageErrf("write <workspaceRoot> <tmpDir> <path>")
		}
		return applyThen(action[1], action[2], func() int { return helperWrite(action[3]) })
	case "read":
		if len(action) != 4 {
			return usageErrf("read <workspaceRoot> <tmpDir> <path>")
		}
		return applyThen(action[1], action[2], func() int { return helperRead(action[3]) })
	case "hardlink":
		if len(action) != 5 {
			return usageErrf("hardlink <workspaceRoot> <tmpDir> <src> <dst>")
		}
		return applyThen(action[1], action[2], func() int { return helperHardlinkWrite(action[3], action[4]) })
	case "symlink":
		if len(action) != 5 {
			return usageErrf("symlink <workspaceRoot> <tmpDir> <linkPath> <target>")
		}
		return applyThen(action[1], action[2], func() int { return helperSymlinkWrite(action[3], action[4]) })
	case "below-abi":
		if len(action) != 3 {
			return usageErrf("below-abi <workspaceRoot> <tmpDir>")
		}
		return helperBelowMinABI(action[1], action[2])
	default:
		return usageErrf(fmt.Sprintf("unknown action %q", action[0]))
	}
}

func usageErrf(msg string) int {
	fmt.Fprintln(os.Stderr, "landlock helper:", msg)
	return exitUsageErr
}

// trimAfterDoubleDash returns the arguments following a bare "--" in args,
// or nil if there is none. Deliberately scans os.Args directly rather than
// relying on the flag package: `go test`'s own -test.* flags precede the
// "--", and the payload after it is attacker-shaped-in-spirit test data
// (paths), not something we want run through flag parsing.
func trimAfterDoubleDash(args []string) []string {
	for i, a := range args {
		if a == "--" {
			return args[i+1:]
		}
	}
	return nil
}

// applyThen installs the real Landlock ruleset for the given roots and,
// only if that succeeds, runs op. This mirrors the frozen apply sequence
// in child_linux.go: a failed applyLandlock must short-circuit before any
// attempt to touch the filesystem on the command's behalf.
func applyThen(workspaceRoot, tmpDir string, op func() int) int {
	if err := applyLandlock(Policy{WorkspaceRoot: workspaceRoot, TmpDir: tmpDir}); err != nil {
		fmt.Fprintf(os.Stderr, "applyLandlock: %v\n", err)
		return exitApplyErr
	}
	return op()
}

func helperWrite(path string) int {
	return classifyFSErr(os.WriteFile(path, []byte("landlock-test"), 0o644))
}

func helperRead(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return classifyFSErr(err)
	}
	fmt.Fprint(os.Stdout, string(data))
	return exitOK
}

func helperHardlinkWrite(src, dst string) int {
	if err := os.Link(src, dst); err != nil {
		return classifyFSErr(err)
	}
	return classifyFSErr(os.WriteFile(dst, []byte("linked-write"), 0o644))
}

func helperSymlinkWrite(linkPath, target string) int {
	if err := os.Symlink(target, linkPath); err != nil {
		return classifyFSErr(err)
	}
	return classifyFSErr(os.WriteFile(linkPath, []byte("symlink-write"), 0o644))
}

// classifyFSErr maps a filesystem op's result onto the helper's exit-code
// vocabulary: nil is success, a permission error is a landlock denial, and
// anything else is an unexpected failure worth surfacing distinctly so a
// failing test's stderr points at the real cause instead of a misread deny.
//
// A denied link(2)/rename(2) that's missing the "refer" access right
// surfaces as EXDEV ("cross-device link"), not EACCES/EPERM — that's the
// kernel's actual Landlock behavior for a reparenting operation across a
// rule boundary, not a real device mismatch (both roots here are always
// on the same filesystem). Recognizing it here is what makes
// TestLandlock_HardlinkOutOfScopeThenWrite_Denied a real assertion about
// SR-3a.8 instead of a false pass/fail on filesystem topology.
func classifyFSErr(err error) int {
	if err == nil {
		return exitOK
	}
	if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EXDEV) {
		return exitDenied
	}
	fmt.Fprintf(os.Stderr, "unexpected filesystem error: %v\n", err)
	return exitUnexpected
}

// helperBelowMinABI simulates a kernel that cannot satisfy Landlock ABI 2
// (or lacks Landlock entirely) by installing a seccomp-bpf filter that
// forces landlock_create_ruleset to fail before applyLandlock ever runs.
// go-landlock's own ABI probe uses that same syscall, so the kernel looks
// exactly like Landlock v0 (absent) to it — this is deterministic and
// independent of whatever ABI the real host kernel happens to support,
// which on any modern dev/CI box is already >= 2 and so can't otherwise
// exercise this floor (SR-3a.8).
func helperBelowMinABI(workspaceRoot, tmpDir string) int {
	if err := blockLandlockSyscalls(); err != nil {
		fmt.Fprintln(os.Stderr, "install seccomp block:", err)
		return exitUsageErr
	}

	err := applyLandlock(Policy{WorkspaceRoot: workspaceRoot, TmpDir: tmpDir})
	if err == nil {
		fmt.Fprintln(os.Stderr, "applyLandlock unexpectedly succeeded with landlock syscalls blocked")
		return exitUnexpected
	}
	fmt.Fprintf(os.Stdout, "applyLandlock failed as expected: %v\n", err)
	return exitApplyErr
}

// blockLandlockSyscalls installs a seccomp-bpf filter that denies only
// landlock_create_ruleset (errno), allowing everything else — enough to
// make the kernel appear Landlock-less to go-landlock's ABI probe without
// disturbing the rest of the helper process.
func blockLandlockSyscalls() error {
	filter := seccomp.Filter{
		NoNewPrivs: true,
		Policy: seccomp.Policy{
			DefaultAction: seccomp.ActionAllow,
			Syscalls: []seccomp.SyscallGroup{
				{
					Names:  []string{"landlock_create_ruleset"},
					Action: seccomp.ActionErrno,
				},
			},
		},
	}
	return seccomp.LoadFilter(filter)
}

// mkSubdirs creates a fresh subdirectory under a single shared t.TempDir()
// for each given name and returns their paths in order.
func mkSubdirs(t *testing.T, names ...string) []string {
	t.Helper()
	root := t.TempDir()
	dirs := make([]string, len(names))
	for i, name := range names {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		dirs[i] = dir
	}
	return dirs
}

func TestLandlock_WriteInsideWorkspace_Succeeds(t *testing.T) {
	ws, tmp := t.TempDir(), t.TempDir()
	target := filepath.Join(ws, "in-workspace.txt")

	if code, _, stderr := runLandlockHelper(t, "write", ws, tmp, target); code != exitOK {
		t.Fatalf("write inside workspace: exit=%d stderr=%s", code, stderr)
	}
}

func TestLandlock_WriteInsideTmp_Succeeds(t *testing.T) {
	ws, tmp := t.TempDir(), t.TempDir()
	target := filepath.Join(tmp, "in-tmp.txt")

	if code, _, stderr := runLandlockHelper(t, "write", ws, tmp, target); code != exitOK {
		t.Fatalf("write inside tmp: exit=%d stderr=%s", code, stderr)
	}
}

func TestLandlock_ReadOutsideScope_Succeeds(t *testing.T) {
	ws, tmp, outside := t.TempDir(), t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "readable.txt")
	if err := os.WriteFile(target, []byte("outside-content"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	code, stdout, stderr := runLandlockHelper(t, "read", ws, tmp, target)
	if code != exitOK {
		t.Fatalf("read outside scope: exit=%d stderr=%s", code, stderr)
	}
	if stdout != "outside-content" {
		t.Fatalf("read outside scope: stdout=%q, want %q", stdout, "outside-content")
	}
}

func TestLandlock_WriteOutsideScope_DeniedEACCES(t *testing.T) {
	ws, tmp, outside := t.TempDir(), t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "denied.txt")

	code, _, stderr := runLandlockHelper(t, "write", ws, tmp, target)
	if code != exitDenied {
		t.Fatalf("write outside scope: exit=%d, want %d (denied); stderr=%s", code, exitDenied, stderr)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Errorf("write outside scope produced a file despite denial: statErr=%v", statErr)
	}
}

func TestLandlock_HardlinkOutOfScopeThenWrite_Denied(t *testing.T) {
	// link(2) requires src and dst to share a filesystem — use one shared
	// root's subdirectories (rather than three independent t.TempDir()
	// calls, which are not guaranteed to share a device) so a genuine
	// landlock denial isn't masked by an unrelated EXDEV.
	dirs := mkSubdirs(t, "ws", "tmp", "outside")
	ws, tmp, outside := dirs[0], dirs[1], dirs[2]
	src := filepath.Join(outside, "source.txt")
	if err := os.WriteFile(src, []byte("source-content"), 0o644); err != nil {
		t.Fatalf("seed source file: %v", err)
	}
	dst := filepath.Join(ws, "linked.txt")

	code, _, stderr := runLandlockHelper(t, "hardlink", ws, tmp, src, dst)
	if code != exitDenied {
		t.Fatalf("hardlink out of scope: exit=%d, want %d (denied); stderr=%s", code, exitDenied, stderr)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Errorf("hardlink out of scope succeeded despite denial: statErr=%v", statErr)
	}
}

func TestLandlock_SymlinkEscape_Denied(t *testing.T) {
	ws, tmp, outside := t.TempDir(), t.TempDir(), t.TempDir()
	link := filepath.Join(ws, "escape-link")
	target := filepath.Join(outside, "escaped.txt")

	code, _, stderr := runLandlockHelper(t, "symlink", ws, tmp, link, target)
	if code != exitDenied {
		t.Fatalf("symlink escape: exit=%d, want %d (denied); stderr=%s", code, exitDenied, stderr)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Errorf("symlink escape produced a file despite denial: statErr=%v", statErr)
	}
}

func TestLandlock_BelowMinABI_FailsClosed(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skipf("landlock syscall-blocking helper not exercised on GOARCH=%s", runtime.GOARCH)
	}

	ws, tmp := t.TempDir(), t.TempDir()

	code, stdout, stderr := runLandlockHelper(t, "below-abi", ws, tmp)
	if code != exitApplyErr {
		t.Fatalf("below-min-ABI: exit=%d, want %d (applyLandlock error); stdout=%s stderr=%s", code, exitApplyErr, stdout, stderr)
	}
}
