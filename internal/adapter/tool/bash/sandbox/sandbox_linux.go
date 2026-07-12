//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

// frameFD and errFD are the fixed fd numbers the re-exec'd child expects
// its two ExtraFiles to land on. os/exec always places fds 0-2 as
// stdin/stdout/stderr, then ExtraFiles[0], ExtraFiles[1], ... sequentially
// — so passing []{frame, err} as ExtraFiles guarantees these numbers.
const (
	frameFD = 3
	errFD   = 4
)

// run is the Linux entrypoint: it re-execs the current binary via
// /proc/self/exe with the reserved ChildSubcommand, delivers {workspace
// root, command} to it over a length-prefixed O_CLOEXEC pipe (T3 framing),
// and reads a second O_CLOEXEC pipe to detect a child setup failure before
// the command ever ran (fail-closed, ADR-0005 §5, SR-3a.13).
func run(ctx context.Context, p Policy, command string, stdout, stderr io.Writer) (int, error) {
	return execSandboxed(ctx, p, command, stdout, stderr, ChildSubcommand)
}

// execSandboxed does the real work behind run. subcommand is the reserved
// argv[1] passed to the re-exec'd child; it is always ChildSubcommand in
// production. Tests in this package pass an alternate marker to exercise
// the fail-closed arm without touching the real apply sequence.
func execSandboxed(ctx context.Context, p Policy, command string, stdout, stderr io.Writer, subcommand string) (int, error) {
	frameR, frameW, err := newCloexecPipe()
	if err != nil {
		return -1, fmt.Errorf("sandbox: create frame pipe: %w", err)
	}
	defer func() { _ = frameR.Close() }()
	defer func() { _ = frameW.Close() }()

	errR, errW, err := newCloexecPipe()
	if err != nil {
		return -1, fmt.Errorf("sandbox: create error pipe: %w", err)
	}
	defer func() { _ = errR.Close() }()
	defer func() { _ = errW.Close() }()

	cmd := exec.CommandContext(ctx, "/proc/self/exe", subcommand)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// ExtraFiles[0] becomes frameFD (3) and ExtraFiles[1] becomes errFD (4)
	// in the child; nothing else is inherited (SR-3a.7 fd hygiene).
	cmd.ExtraFiles = []*os.File{frameR, errW}

	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("sandbox: start re-exec child: %w", err)
	}

	// The child now holds its own dup'd copies of frameR/errW. Close the
	// parent's originals: the parent never reads frameR or writes errW, and
	// closing errW here means errR sees a true EOF as soon as the child's
	// own copy of errW closes (rather than also waiting on this one).
	_ = frameR.Close()
	_ = errW.Close()

	frameErr := writeFrame(frameW, p.WorkspaceRoot, p.TmpDir, command)
	_ = frameW.Close()
	if frameErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return -1, fmt.Errorf("sandbox: write frame: %w", frameErr)
	}

	setupErr, readErr := io.ReadAll(errR)
	if readErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return -1, fmt.Errorf("sandbox: read error pipe: %w", readErr)
	}

	waitErr := cmd.Wait()

	if len(setupErr) > 0 {
		// Fail-closed: the child reported a setup failure before it ever
		// execve'd into bash — the command never ran (SR-3a.10).
		return -1, fmt.Errorf("%w: %s", ErrUnsupported, bytes.TrimSpace(setupErr))
	}

	return exitCodeFrom(waitErr)
}

// exitCodeFrom maps cmd.Wait's error into (exit code, nil) for a normal
// process exit (zero or non-zero), or (-1, err) for anything that isn't a
// plain exit — a launch/wait infrastructure failure.
func exitCodeFrom(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return -1, fmt.Errorf("sandbox: wait re-exec child: %w", err)
}

// newCloexecPipe opens a pipe with O_CLOEXEC set on both ends, so neither
// fd survives an unrelated exec in this process before it is explicitly
// handed to the intended child via ExtraFiles.
func newCloexecPipe() (r, w *os.File, err error) {
	var fds [2]int
	if err := unix.Pipe2(fds[:], unix.O_CLOEXEC); err != nil {
		return nil, nil, err
	}
	return os.NewFile(uintptr(fds[0]), "sandbox-pipe-r"), os.NewFile(uintptr(fds[1]), "sandbox-pipe-w"), nil
}
