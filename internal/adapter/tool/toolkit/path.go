// Package toolkit is the single shared home of the two cross-cutting tool
// concerns: SR-5 tool-argument validation (validate.go), SR-2 workspace path
// containment (this file), and the frozen tool-result envelope convention
// (result.go). Every built-in tool (read/write/edit/bash) imports this
// package; nothing outside internal/adapter/tool imports it.
package toolkit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrPathEscape is returned by ResolvePath when argPath — after joining
// against workspaceRoot and resolving any symlinks — would resolve outside
// workspaceRoot. It is the sentinel every caller checks with errors.Is,
// never a panic.
var ErrPathEscape = errors.New("path escapes workspace")

// ResolvePath resolves argPath relative to workspaceRoot and guarantees the
// result is contained within workspaceRoot (SR-2). It rejects the empty
// path, absolute paths, "../" escapes, and symlinks that resolve outside
// the root — returning ErrPathEscape in every rejection case, never a
// panic. On success it returns the cleaned, absolute, symlink-resolved
// path.
func ResolvePath(workspaceRoot, argPath string) (string, error) {
	if argPath == "" || filepath.IsAbs(argPath) {
		return "", ErrPathEscape
	}

	joined := filepath.Join(workspaceRoot, argPath)
	clean := filepath.Clean(joined)

	rootResolved, err := resolveExistingAncestor(workspaceRoot)
	if err != nil {
		return "", err
	}

	resolved, err := resolveDefensive(clean)
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(rootResolved, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", ErrPathEscape
	}

	return resolved, nil
}

// resolveDefensive resolves symlinks on the longest existing ancestor of
// path, then re-joins the non-existent remainder (if any) — so a
// not-yet-created write target still gets its existing ancestors checked
// for symlink escapes.
func resolveDefensive(path string) (string, error) {
	ancestorResolved, remainder, err := splitExistingAncestor(path)
	if err != nil {
		return "", err
	}
	if remainder == "" {
		return ancestorResolved, nil
	}
	return filepath.Join(ancestorResolved, remainder), nil
}

// resolveExistingAncestor resolves symlinks on the longest existing
// ancestor of path (path itself, if it exists).
func resolveExistingAncestor(path string) (string, error) {
	resolved, _, err := splitExistingAncestor(path)
	return resolved, err
}

// splitExistingAncestor walks up from path until it finds an ancestor that
// exists, resolves that ancestor's symlinks, and returns the resolved
// ancestor plus the remainder path (relative, possibly empty) that did not
// exist.
func splitExistingAncestor(path string) (resolvedAncestor string, remainder string, err error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}

	current := abs
	var remainderParts []string
	for {
		resolved, statErr := filepath.EvalSymlinks(current)
		if statErr == nil {
			rem := ""
			if len(remainderParts) > 0 {
				reversed := make([]string, len(remainderParts))
				for i, p := range remainderParts {
					reversed[len(remainderParts)-1-i] = p
				}
				rem = filepath.Join(reversed...)
			}
			return resolved, rem, nil
		}
		if !os.IsNotExist(statErr) {
			return "", "", statErr
		}

		parent := filepath.Dir(current)
		if parent == current {
			// Reached filesystem root without finding anything that exists.
			return "", "", statErr
		}
		remainderParts = append(remainderParts, filepath.Base(current))
		current = parent
	}
}
