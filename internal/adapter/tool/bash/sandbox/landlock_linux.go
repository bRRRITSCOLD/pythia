//go:build linux

package sandbox

import (
	"fmt"

	"github.com/landlock-lsm/go-landlock/landlock"
)

// landlockReadRoot is the path granted broad, read-only access. Read is
// intentionally not scoped to the workspace: only write is confined
// (plan Task 6, threat model §2.2).
const landlockReadRoot = "/"

// applyLandlock installs a Landlock filesystem ruleset that confines the
// calling thread (and, once TSYNC'd by the kernel's ABI, the whole process)
// to:
//   - broad read access under landlockReadRoot ("/"), and
//   - read+write+refer access under p.WorkspaceRoot and p.TmpDir only.
//
// It is pinned to Landlock ABI >= 2 (landlock.V2), which adds the "refer"
// access right — required to deny the classic hardlink/rename escape (link
// or rename a file from an unwritable directory into a writable one, then
// write through the new name). BestEffort() is deliberately never used:
// on a kernel that cannot fully satisfy landlock.V2 (below ABI 2, Landlock
// disabled at boot, or absent entirely), Config.RestrictPaths returns a
// non-nil error instead of silently downgrading, and applyLandlock
// propagates it so the caller fails closed (ADR-0005 §5, SR-3a.8).
func applyLandlock(p Policy) error {
	rules := []landlock.Rule{
		landlock.RODirs(landlockReadRoot),
	}

	for _, root := range writeRoots(p) {
		rules = append(rules, landlock.RWDirs(root).WithRefer())
	}

	if err := landlock.V2.RestrictPaths(rules...); err != nil {
		return fmt.Errorf("sandbox: landlock restrict (ABI>=2 required): %w", err)
	}
	return nil
}

// writeRoots returns the distinct, non-empty write-scope roots from p. A
// caller that leaves TmpDir unset (or equal to WorkspaceRoot) still gets a
// valid ruleset — landlock.RWDirs on an empty path list is simply omitted
// rather than passed through as a rule granting access to "".
func writeRoots(p Policy) []string {
	var roots []string
	seen := make(map[string]bool, 2)
	for _, root := range []string{p.WorkspaceRoot, p.TmpDir} {
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		roots = append(roots, root)
	}
	return roots
}
