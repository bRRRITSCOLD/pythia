//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

// runChild is the re-exec child's entrypoint body (reached via the
// exported RunChild, invoked by cmd/pythia/main.go's reserved-subcommand
// hook when os.Args[1] == ChildSubcommand). It delegates to
// runChildWithApply using the package's real applyLandlock/applySeccomp,
// so production always runs the frozen sequence below.
func runChild() int {
	return runChildWithApply(applyLandlock, applySeccomp)
}

// runChildWithApply is runChild's body, parameterized over the
// Landlock/seccomp apply steps so this package's own tests can substitute
// an alternate applySeccomp (e.g. while T7/#103 is still a fail-closed
// stub) to exercise the rest of the frozen sequence — thread lock, frame
// read, chdir, NO_NEW_PRIVS, env scrub, Landlock, fd hygiene, exec — without
// waiting on T7. Production always calls this via runChild with the real
// applyLandlock and applySeccomp; the sequence itself is unchanged and
// frozen (plan Task 5 "Interfaces", decision 5).
//
// It never returns on success — syscall.Exec replaces this process image
// with /bin/bash. On any setup failure it reports the error over the
// reserved error-pipe fd and returns non-zero without ever reading the
// command far enough to run it (fail-closed, ADR-0005 §5).
//
// The apply sequence: lock the OS thread (never unlocked — the thread's
// NO_NEW_PRIVS bit and any per-thread LSM state set below must never leak
// back into a pooled goroutine thread) -> read the frame -> close the
// frame fd -> chdir into the workspace root (SR-3b: the confined command
// must observe the same working directory as the legacy direct-exec path)
// -> NO_NEW_PRIVS -> scrub env (T4) -> applyLandlockFn (T6) ->
// applySeccompFn (T7, TSYNC'd last when real) -> exec. There is
// deliberately no on/off branch in this file (SR-3a.11) — that decision
// lives only in the parent (T8).
func runChildWithApply(applyLandlockFn func(Policy) error, applySeccompFn func() error) int {
	runtime.LockOSThread()

	errFile := os.NewFile(uintptr(errFD), "sandbox-err")

	fail := func(cause error) int {
		if errFile != nil {
			fmt.Fprintln(errFile, cause.Error())
			_ = errFile.Close()
		}
		return 1
	}

	frameFile := os.NewFile(uintptr(frameFD), "sandbox-frame")
	if frameFile == nil {
		return fail(fmt.Errorf("sandbox: frame fd %d unavailable", frameFD))
	}

	root, tmpDir, command, err := readFrame(frameFile)
	_ = frameFile.Close()
	if err != nil {
		return fail(fmt.Errorf("sandbox: read frame: %w", err))
	}

	// The confined command must run with the same working directory
	// contract as the legacy direct-exec path (cmd.Dir = t.workDir) — the
	// re-exec'd child otherwise inherits pythia's own cwd instead of the
	// configured workspace root, breaking any relative-path usage
	// (SR-3b).
	if err := os.Chdir(root); err != nil {
		return fail(fmt.Errorf("sandbox: chdir %s: %w", root, err))
	}

	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fail(fmt.Errorf("sandbox: set NO_NEW_PRIVS: %w", err))
	}

	env := scrubEnv(os.Environ())

	if err := applyLandlockFn(Policy{WorkspaceRoot: root, TmpDir: tmpDir}); err != nil {
		return fail(fmt.Errorf("sandbox: apply landlock: %w", err))
	}
	if err := applySeccompFn(); err != nil {
		return fail(fmt.Errorf("sandbox: apply seccomp: %w", err))
	}

	// The error pipe must never reach bash (SR-3a.7 fd hygiene). Marking it
	// close-on-exec here — rather than closing it outright — means a
	// successful execve below closes it for us as part of the exec syscall
	// itself, signalling success to the parent with a clean EOF; if Exec
	// instead returns (a failure), the fd is still open and fail() below
	// can still report it.
	unix.CloseOnExec(errFD)

	if err := syscall.Exec(bashPath, []string{"bash", "-c", command}, env); err != nil {
		return fail(fmt.Errorf("sandbox: exec %s: %w", bashPath, err))
	}
	return 0 // unreachable: syscall.Exec only returns on failure
}
