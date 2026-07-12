//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"

	seccomp "github.com/elastic/go-seccomp-bpf"
	"github.com/landlock-lsm/go-landlock/landlock"
	"golang.org/x/sys/unix"
)

// The three sandbox dependencies are anchored here so `go mod tidy` keeps
// them required under the linux build tag even before the real spine (T5)
// exercises them. They are replaced by real usage as later tasks land:
// landlock (T6, filesystem confinement), seccomp-bpf (T7, syscall
// allowlist), x/sys/unix (T5/T6/T7, low-level primitives — NO_NEW_PRIVS,
// close-on-exec, syscall.Exec's underlying numbers).
var (
	_ = landlock.V2
	_ = seccomp.Filter{}
	_ = unix.Getpid
)

// run is the Linux entrypoint. TODO(T5): install the Landlock ruleset (T6)
// and seccomp-bpf filter (T7) in a re-exec'd child, then stream that
// child's stdout/stderr and wait for its exit code. Until the spine lands
// this deliberately still fails closed — nothing here may run a command
// unsandboxed, even on Linux (ADR-0005 §5).
func run(_ context.Context, _ Policy, _ string, _, _ io.Writer) (exitCode int, err error) {
	return -1, ErrUnsupported
}

// runChild is the re-exec child entrypoint. TODO(T5): set NO_NEW_PRIVS,
// scrub env to the fixed allowlist (T4), read the framed policy+command
// off the pipe (T3), install Landlock (T6) then seccomp (T7) on a locked
// OS thread, and syscall.Exec into /bin/bash. Until the spine lands this
// deliberately still fails closed.
func runChild() int {
	fmt.Fprintln(os.Stderr, ErrUnsupported.Error())
	return 1
}
