// Package sandbox provides OS-level isolation for the bash tool's
// subprocess (ADR-0005, docs/security/bash-sandbox-threat-model.md).
//
// On Linux, Run installs a Landlock filesystem policy plus a seccomp-bpf
// syscall allowlist in a re-exec'd child before it execve's into /bin/bash
// (RunChild is that child's entrypoint, invoked via a reserved main.go
// subcommand — see ADR-0005 §3). On every other platform, both entrypoints
// fail closed with ErrUnsupported: no command ever runs unsandboxed.
//
// This package is the entire OS-sandbox perimeter for the bash tool; the
// exported surface (Policy, ErrUnsupported, Run, RunChild) is frozen and
// documented in docs/superpowers/plans/bash-sandbox.md (Task 2). Everything
// else here is an internal implementation detail, filled in by later tasks
// on the plan's critical path (T3 wire framing, T4 env scrub, T5 the spine,
// T6/T7 the Landlock/seccomp controls).
package sandbox
