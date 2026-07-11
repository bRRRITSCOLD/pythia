// Package bash implements the "bash" built-in tool: it runs a shell command
// in a subprocess bound by a configured working directory, a context
// timeout, and a bounded output buffer (SR-3, SR-4c, SR-5). The boundary is
// isolated behind core.Tool so a future OS sandbox (SR-3a) can drop in
// behind this same seam.
package bash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"time"

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

// bashTool is the core.Tool implementation. workDir, timeout, and
// maxOutputBytes are fixed at construction (SR-3b, SR-3, SR-4c) — nothing in
// Invoke's args can override them (SR-5).
type bashTool struct {
	workDir        string
	timeout        time.Duration
	maxOutputBytes int64
}

// New constructs the bash tool bound to a fixed workDir, a per-invocation
// timeout, and a bounded output cap in bytes.
func New(workDir string, timeout time.Duration, maxOutputBytes int64) core.Tool {
	return &bashTool{
		workDir:        workDir,
		timeout:        timeout,
		maxOutputBytes: maxOutputBytes,
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

// Invoke runs the requested command. A Go error is returned only for an
// exec-launch failure (infrastructure failure); malformed args, a non-zero
// exit, a timeout, and truncated output are all soft results carried in the
// returned envelope so the turn loop can feed them back to the model.
func (t *bashTool) Invoke(ctx context.Context, rawArgs json.RawMessage) (json.RawMessage, error) {
	var a args
	if err := toolkit.Validate(rawArgs, &a); err != nil {
		return toolkit.Err("bash: invalid args: %v", err), nil
	}

	ctx2, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx2, "bash", "-c", a.Command)
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

	if ctx2.Err() == context.DeadlineExceeded {
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
