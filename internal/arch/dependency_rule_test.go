package arch

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// goListPackage mirrors the subset of `go list -json` output we need: the
// package's import path and its direct imports (excluding test files, which
// is a known, documented limitation of this guard).
type goListPackage struct {
	ImportPath string
	Imports    []string
}

// TestCoreImportsOnlyStdlib is the load-bearing fitness function for the
// dependency rule (docs/adr/0004): no package anywhere under internal/core
// may import an internal/adapter/* package or any third-party package —
// only the standard library or another package within internal/core itself.
//
// It walks the whole internal/core subtree (`go list ./internal/core/...`)
// rather than only the top-level internal/core package, because the planned
// layout (docs/architecture/first-slice.md) splits core into subpackages
// such as internal/core/agent, internal/core/domain, etc. — no .go files
// live directly in internal/core, so inspecting only that single package
// would never detect a leak in any of its subpackages.
//
// It is expected to be RED until internal/core exists (task T2); from then
// on it is the permanent guard that fails loudly the moment any core
// subpackage gains a forbidden import.
func TestCoreImportsOnlyStdlib(t *testing.T) {
	const module = "github.com/bRRRITSCOLD/pythia"
	const corePrefix = module + "/internal/core"

	// Use the fully-qualified module pattern rather than a relative
	// "./internal/core/..." pattern: `go test` runs with the package
	// directory (internal/arch) as the working directory, not the module
	// root, so a relative pattern would fail to resolve regardless of
	// whether internal/core exists.
	out, err := exec.Command("go", "list", "-json", corePrefix+"/...").Output()
	if err != nil {
		t.Fatalf("internal/core not importable yet (expected until T2 lands): %v", err)
	}

	dec := json.NewDecoder(strings.NewReader(string(out)))
	sawPackage := false
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("failed to decode `go list -json` output: %v", err)
		}
		sawPackage = true

		for _, imp := range pkg.Imports {
			// Allowed: imports within core's own subtree.
			if imp == corePrefix || strings.HasPrefix(imp, corePrefix+"/") {
				continue
			}
			if strings.HasPrefix(imp, module+"/internal/adapter") {
				t.Errorf("%s imports adapter %q", pkg.ImportPath, imp)
				continue
			}
			// Check in-module BEFORE isThirdParty: module-internal paths also
			// start with a dotted first segment (github.com/...), so isThirdParty
			// would report them as third-party and this branch would be dead.
			if strings.HasPrefix(imp, module+"/") {
				t.Errorf("%s imports disallowed in-module package %q", pkg.ImportPath, imp)
				continue
			}
			if isThirdParty(imp) {
				t.Errorf("%s imports third-party %q", pkg.ImportPath, imp)
			}
		}
	}

	if !sawPackage {
		t.Fatalf("internal/core not importable yet (expected until T2 lands): go list returned no packages")
	}
}

// isThirdParty reports whether imp is a third-party (non-stdlib) import
// path. Standard library import paths never contain a dot in their first
// path segment (e.g. "net/http", "encoding/json"), whereas third-party
// paths do (e.g. "golang.org/x/tools", "github.com/foo/bar"). This check
// only makes sense once module-internal paths have already been ruled out
// by the caller, since module paths also contain dots.
func isThirdParty(imp string) bool {
	first := imp
	if idx := strings.Index(imp, "/"); idx != -1 {
		first = imp[:idx]
	}
	return strings.Contains(first, ".")
}
