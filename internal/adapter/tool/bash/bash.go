// Package bash implements the "bash" built-in tool: it runs a shell command
// in a subprocess bound by a configured working directory, a context
// timeout, and a bounded output buffer (SR-3, SR-4c, SR-5). The boundary is
// isolated behind core.Tool so the OS sandbox (SR-3a, see the sandbox
// subpackage and ADR-0005) drops in behind this same seam.
//
// The sandbox cannot close every risk — a sandboxed command's stdout is
// still returned to the model. That residual risk, the load-bearing
// local-Ollama assumption it depends on, and the deferred hardening items
// (rlimits, denied-syscall observability) are documented in
// docs/security/bash-sandbox-residual-risk.md and
// docs/security/bash-sandbox-threat-model.md.
package bash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/bRRRITSCOLD/pythia/internal/adapter/tool/bash/sandbox"
	"github.com/bRRRITSCOLD/pythia/internal/adapter/tool/toolkit"
	"github.com/bRRRITSCOLD/pythia/internal/core"
)

// paramsSchema is the frozen JSON Schema advertised to the Provider,
// describing this tool's single required "command" argument.
const paramsSchema = `{
  "type": "object",
  "properties": {
    "command": {
      "type": "string",
      "description": "The shell command to run."
    }
  },
  "required": ["command"],
  "additionalProperties": false
}`

// args is the validated shape of this tool's invocation arguments.
type args struct {
	Command string `json:"command" validate:"required"`
}

// output is the frozen ok-envelope payload for this tool.
type output struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated"`
	TimedOut  bool   `json:"timed_out"`
}

// bashTool is the core.Tool implementation. workDir, timeout,
// maxOutputBytes, and sandbox are fixed at construction (SR-3b, SR-3,
// SR-4c) — nothing in Invoke's args can override them (SR-5). sandbox is a
// plain bool decided by the parent (cmd/pythia/main.go, from
// cfg.BashSandbox) — this package intentionally does not import config, so
// the on/off decision is handed in as a value rather than resolved here
// (keeps the adapter layer free of config coupling, verified by
// `make arch-test`).
type bashTool struct {
	workDir        string
	timeout        time.Duration
	maxOutputBytes int64
	sandbox        bool

	// logUnsandboxedOnce ensures the "bash sandbox DISABLED" repudiation-
	// control log (SR-3a.11) is emitted at most once per tool instance, no
	// matter how many times Invoke runs the legacy off-branch.
	logUnsandboxedOnce sync.Once

	// runSandboxed defaults to sandbox.Run; overridable only in this
	// package's own tests, so the fail-closed envelope-mapping behavior in
	// invokeSandboxed can be exercised deterministically on any GOOS
	// without depending on sandbox.ErrUnsupported's platform-specific
	// trigger conditions.
	runSandboxed func(ctx context.Context, p sandbox.Policy, command string, stdout, stderr io.Writer) (int, error)
}

// defaultRunSandboxed is sandbox.Run, captured at package scope so New's
// "sandbox bool" parameter (its name frozen by the plan/issue's interface
// spec) can shadow the sandbox package name inside its own body without
// losing the ability to reference sandbox.Run.
var defaultRunSandboxed = sandbox.Run

// New constructs the bash tool bound to a fixed workDir, a per-invocation
// timeout, a bounded output cap in bytes, and the sandbox on/off decision.
func New(workDir string, timeout time.Duration, maxOutputBytes int64, sandbox bool) core.Tool {
	return &bashTool{
		workDir:        workDir,
		timeout:        timeout,
		maxOutputBytes: maxOutputBytes,
		sandbox:        sandbox,
		runSandboxed:   defaultRunSandboxed,
	}
}

// Schema advertises this tool's name, description, and JSON-Schema
// parameters to the Provider.
func (t *bashTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name:        "bash",
		Description: "Runs a shell command in a subprocess, bound by a fixed working directory, a timeout, and a bounded output buffer.",
		Parameters:  json.RawMessage(paramsSchema),
	}
}

// Invoke runs the requested command, routed through the OS sandbox when
// t.sandbox is on and via the legacy direct-exec path when it is off. A Go
// error is returned only for an exec-launch failure (infrastructure
// failure); malformed args, a non-zero exit, a timeout, truncated output,
// and a sandbox setup failure are all soft results carried in the returned
// envelope so the turn loop can feed them back to the model.
func (t *bashTool) Invoke(ctx context.Context, rawArgs json.RawMessage) (json.RawMessage, error) {
	var a args
	if err := toolkit.Validate(rawArgs, &a); err != nil {
		return toolkit.Err("bash: invalid args: %v", err), nil
	}

	ctx2, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	if t.sandbox {
		return t.invokeSandboxed(ctx2, a.Command)
	}
	return t.invokeUnsandboxed(ctx2, a.Command)
}

// invokeSandboxed routes command through the sandbox package (SR-3a.10).
// Any setup error — including sandbox.ErrUnsupported — means the command
// never ran: this returns a soft error envelope, never a partial result,
// and never falls back to the unsandboxed path (fail-closed).
func (t *bashTool) invokeSandboxed(ctx context.Context, command string) (json.RawMessage, error) {
	stdout := newLimitedBuffer(t.maxOutputBytes)
	stderr := newLimitedBuffer(t.maxOutputBytes)

	policy := sandbox.Policy{WorkspaceRoot: t.workDir, TmpDir: os.TempDir()}

	exitCode, err := t.runSandboxed(ctx, policy, command, stdout, stderr)
	if err != nil {
		// Fail-closed: the sandbox could not be set up (or is unsupported
		// on this platform/kernel) — the command was never executed.
		return toolkit.Err("bash: sandbox unavailable, command not run: %v", err), nil
	}

	out := output{
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		ExitCode:  exitCode,
		Truncated: stdout.truncated || stderr.truncated,
	}
	if ctx.Err() == context.DeadlineExceeded {
		out.TimedOut = true
	}
	return toolkit.OK(out), nil
}

// invokeUnsandboxed is the legacy direct-exec path, used only when the
// sandbox is explicitly disabled (SR-3a.11). It emits a one-time "bash
// sandbox DISABLED" log (repudiation control) the first time it runs on
// this tool instance.
func (t *bashTool) invokeUnsandboxed(ctx context.Context, command string) (json.RawMessage, error) {
	t.logUnsandboxedOnce.Do(func() {
		slog.Warn("bash sandbox DISABLED: running commands unsandboxed", "reason", "PYTHIA_BASH_SANDBOX=off")
	})

	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = t.workDir
	// SR-3c: only the parent process's own env — nothing is added, nothing
	// from args is merged in, so no extra secrets ever reach the subprocess.
	cmd.Env = os.Environ()

	stdout := newLimitedBuffer(t.maxOutputBytes)
	stderr := newLimitedBuffer(t.maxOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()

	out := output{
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		Truncated: stdout.truncated || stderr.truncated,
	}

	if ctx.Err() == context.DeadlineExceeded {
		out.TimedOut = true
	}

	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		out.ExitCode = 0
	case errors.As(runErr, &exitErr):
		out.ExitCode = exitErr.ExitCode()
	case out.TimedOut:
		// The process was killed for exceeding its deadline; there is no
		// meaningful exit code to report beyond the timed_out flag.
		out.ExitCode = -1
	default:
		// Launch failure (e.g. "bash" not found) — an infrastructure error,
		// not something the model can act on by adjusting its command.
		return nil, newExecLaunchError(runErr)
	}

	return toolkit.OK(out), nil
}

// newExecLaunchError wraps a launch failure with context, kept as a tiny
// named helper so the switch above stays readable.
func newExecLaunchError(cause error) error {
	return &execLaunchError{cause: cause}
}

type execLaunchError struct{ cause error }

func (e *execLaunchError) Error() string { return "bash: exec launch failed: " + e.cause.Error() }
func (e *execLaunchError) Unwrap() error { return e.cause }

// limitedBuffer is a bytes.Buffer that caps writes at max bytes and flags
// truncated once any write is dropped (SR-4c). Writing never errors — a
// tool's stdout/stderr pipe must never fail because it filled up faster
// than we could grow an unbounded buffer.
type limitedBuffer struct {
	buf       bytes.Buffer
	max       int64
	truncated bool
}

func newLimitedBuffer(max int64) *limitedBuffer {
	return &limitedBuffer{max: max}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.max - int64(b.buf.Len())
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }
