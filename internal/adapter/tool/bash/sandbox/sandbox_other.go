//go:build !linux

package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
)

// run is the non-Linux stub: the sandbox mechanism (Landlock + seccomp) is
// Linux-only, so every other platform fails closed — the command is never
// executed (ADR-0005 §5, SR-3a.10).
func run(_ context.Context, _ Policy, _ string, _, _ io.Writer) (exitCode int, err error) {
	return -1, ErrUnsupported
}

// runChild mirrors run's fail-closed posture for the re-exec child path:
// on a non-Linux platform there is no sandbox to install, so it reports
// ErrUnsupported and exits non-zero rather than falling back to an
// unsandboxed exec.
func runChild() int {
	fmt.Fprintln(os.Stderr, ErrUnsupported.Error())
	return 1
}
