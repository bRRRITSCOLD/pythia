//go:build linux

package sandbox

import seccomp "github.com/elastic/go-seccomp-bpf"

// Anchors the seccomp dependency under the linux build tag so `go mod
// tidy` keeps it required before applySeccomp has a real implementation to
// reference it.
var _ = seccomp.Filter{}

// applySeccomp is a no-op stub: it installs no syscall filter. Filled in by
// T7, which installs a real seccomp-bpf allowlist (TSYNC'd across the
// process, applied last in the frozen sequence in child_linux.go). Safe as
// a no-op only because nothing calls the spine yet — bashTool.Invoke is
// wired in T8, after T6 and T7 both land.
//
// TODO(T7): install the real seccomp-bpf allowlist filter.
func applySeccomp() error {
	return nil
}
