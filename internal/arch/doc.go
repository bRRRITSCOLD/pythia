// Package arch holds architecture fitness functions for Pythia.
//
// These are not unit tests of behavior — they are continuously-verified
// guards on the structural invariants described in
// docs/adr/0004-module-package-layout-dependency-rule.md:
//
//   - The dependency rule: internal/core imports only the standard library
//     (no internal/adapter/*, no third-party package).
//   - The CGO-free build (enforced via `make check-cgo`, not in this
//     package).
//
// A failure here means an architectural invariant has been violated, not
// that a feature is broken.
package arch
