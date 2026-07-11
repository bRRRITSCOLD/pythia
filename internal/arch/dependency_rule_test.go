package arch

import (
	"go/build"
	"strings"
	"testing"
)

// Core_Package_ImportsOnlyStdlib is the load-bearing fitness function for the
// dependency rule (docs/adr/0004): internal/core must never import an
// internal/adapter/* package or any third-party (dotted import path)
// package — only the standard library.
//
// It is expected to be RED until internal/core exists (task T2); from then
// on it is the permanent guard that fails loudly the moment core gains a
// forbidden import.
func Core_Package_ImportsOnlyStdlib(t *testing.T) {
	const module = "github.com/bRRRITSCOLD/pythia"

	pkg, err := build.Import(module+"/internal/core", "", 0)
	if err != nil {
		t.Fatalf("internal/core not importable yet (expected until T2 lands): %v", err)
	}

	for _, imp := range pkg.Imports {
		if strings.HasPrefix(imp, module+"/internal/adapter") {
			t.Errorf("core imports adapter %q", imp)
		}
		if strings.Contains(imp, ".") {
			t.Errorf("core imports third-party %q", imp)
		}
	}
}

func TestCore_Package_ImportsOnlyStdlib(t *testing.T) {
	Core_Package_ImportsOnlyStdlib(t)
}
