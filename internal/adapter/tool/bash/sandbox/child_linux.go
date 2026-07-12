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
// hook when os.Args[1] == ChildSubcommand). It never returns on success —
// syscall.Exec replaces this process image with /bin/bash. On any setup
// failure it reports the error over the reserved error-pipe fd and returns
// non-zero without ever reading the command far enough to run it
// (fail-closed, ADR-0005 §5).
//
// The apply sequence is frozen (plan Task 5 "Interfaces", decision 5):
// lock the OS thread (never unlocked — the thread's NO_NEW_PRIVS bit and
// any per-thread LSM state set below must never leak back into a pooled
// goroutine thread) -> read the frame -> close the frame fd -> NO_NEW_PRIVS
// -> scrub env (T4) -> applyLandlock (T6, no-op stub here) -> applySeccomp
// (T7, no-op stub here, TSYNC'd last when real) -> exec. There is
// deliberately no on/off branch in this file (SR-3a.11) — that decision
// lives only in the parent (T8).
func runChild() int {
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

	root, command, err := readFrame(frameFile)
	_ = frameFile.Close()
	if err != nil {
		return fail(fmt.Errorf("sandbox: read frame: %w", err))
	}

	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fail(fmt.Errorf("sandbox: set NO_NEW_PRIVS: %w", err))
	}

	env := scrubEnv(os.Environ())

	if err := applyLandlock(Policy{WorkspaceRoot: root}); err != nil {
		return fail(fmt.Errorf("sandbox: apply landlock: %w", err))
	}
	if err := applySeccomp(); err != nil {
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
