//go:build linux

package sandbox

import "github.com/landlock-lsm/go-landlock/landlock"

// Anchors the landlock dependency under the linux build tag so `go mod
// tidy` keeps it required before applyLandlock has a real implementation to
// reference it.
var _ = landlock.V2

// applyLandlock is a no-op stub: it grants no additional filesystem
// confinement. Filled in by T6, which installs a real Landlock ABI>=2
// ruleset restricting writes to p.WorkspaceRoot and p.TmpDir. Safe as a
// no-op only because nothing calls the spine yet — bashTool.Invoke is
// wired in T8, after T6 and T7 both land (see child_linux.go).
//
// TODO(T6): install the real Landlock ruleset; fail closed below ABI 2.
func applyLandlock(p Policy) error {
	return nil
}
