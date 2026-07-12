//go:build linux

package sandbox

import (
	"errors"

	seccomp "github.com/elastic/go-seccomp-bpf"
)

// Anchors the seccomp dependency under the linux build tag so `go mod
// tidy` keeps it required before applySeccomp has a real implementation to
// reference it.
var _ = seccomp.Filter{}

// errSeccompNotImplemented is returned by applySeccomp until T7 (#103)
// lands a real seccomp-bpf allowlist. T8 (#104) is the first caller of the
// spine, so a silent no-op here would let every sandboxed command run with
// NO_NEW_PRIVS + Landlock write-scoping but NO syscall filter — confinement
// that looks complete but is not (ADR-0005 §5, SR-3a fail-closed). Failing
// closed here means the sandboxed path errors out (the bash tool returns a
// soft "sandbox unavailable" result, never a silent unsandboxed run) until
// the real filter replaces this stub.
var errSeccompNotImplemented = errors.New("sandbox: seccomp not yet implemented (T7/#103); refusing to run with a partial sandbox")

// applySeccomp is a fail-closed stub: it installs no syscall filter and
// refuses to proceed, rather than silently presenting as sandboxed. Filled
// in by T7, which installs a real seccomp-bpf allowlist (TSYNC'd across the
// process, applied last in the frozen sequence in child_linux.go).
//
// TODO(T7): install the real seccomp-bpf allowlist filter and remove this
// guard.
func applySeccomp() error {
	return errSeccompNotImplemented
}
