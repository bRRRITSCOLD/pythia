package sandbox

import (
	"context"
	"errors"
	"io"
)

// Policy is the sandbox's write-scope configuration: the two roots the
// confined command may write to. Everything else on the filesystem is
// read-only (or inaccessible) by default. Policy is built by the bash tool
// from its fixed workDir + the OS temp dir at Invoke time — nothing in the
// model's tool arguments can widen it (SR-5).
type Policy struct {
	// WorkspaceRoot is the bash tool's configured working directory. The
	// confined command may read and write anywhere under this root.
	WorkspaceRoot string
	// TmpDir is the writable scratch root (e.g. the OS temp dir). The
	// confined command may read and write anywhere under this root.
	TmpDir string
}

// ChildSubcommand is the reserved argv[1] cmd/pythia/main.go dispatches
// straight to RunChild, before any other startup work (config.Load, the
// TUI). It is a fixed marker only — the shell command bytes themselves are
// never carried on argv, only over the out-of-band pipe (ADR-0005 §3,
// SR-3a.13). Defined once here (no build tag) so main.go and the Linux
// spine share a single source of truth instead of duplicating the literal.
const ChildSubcommand = "__bash-sandbox"

// ErrUnsupported is returned by Run and reported by RunChild when the OS
// sandbox cannot be enforced on the current platform or kernel — including
// every non-Linux GOOS, and (once T5 lands) a Linux kernel older than 5.13
// or without Landlock ABI >= 2. The bash tool must fail closed on this
// error: the command is never run unsandboxed (ADR-0005 §5).
var ErrUnsupported = errors.New("bash sandbox unsupported on this platform/kernel")

// Run executes command under the sandbox described by p, streaming its
// stdout/stderr to the given writers, and returns its exit code.
//
// A non-nil error means the command did not run at all (setup/launch
// failure, including ErrUnsupported) — it is never returned alongside a
// meaningful exit code. Implementation lives in sandbox_linux.go (real,
// filled in by T5) and sandbox_other.go (fail-closed stub).
func Run(ctx context.Context, p Policy, command string, stdout, stderr io.Writer) (exitCode int, err error) {
	return run(ctx, p, command, stdout, stderr)
}

// RunChild is the re-exec child's entrypoint: it is invoked by
// cmd/pythia/main.go when the process is launched with the reserved
// sandbox subcommand (ADR-0005 §3). It never returns to its caller on
// success — it installs the sandbox controls and execve's into /bin/bash.
// It returns a non-zero exit code only on setup failure, including on
// platforms where the sandbox is unsupported.
func RunChild() int {
	return runChild()
}
