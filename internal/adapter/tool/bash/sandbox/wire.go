// Package sandbox contains the out-of-band delivery format used to hand a
// resolved workspace root and an attacker-controlled command string across a
// process boundary (e.g. to a re-exec'd child that applies OS-level sandbox
// policy before running the command; SR-3a).
//
// Framing is length-prefixed, never delimiter-based: the command bytes are
// attacker-controlled and may contain any byte value including newlines,
// NUL, and shell metacharacters, so a delimiter-based frame could be
// desynchronised by an attacker crafting the command (threat model §2.4,
// SR-3a.13). This file is pure encode/decode over io.Reader/io.Writer — no
// syscalls, no build tag — so it is fully unit-testable on any OS.
package sandbox

import (
	"encoding/binary"
	"fmt"
	"io"
)

// maxFrameFieldBytes bounds a single length-prefixed field (workspace root
// or command) to a few MiB. This rejects an absurd length claim outright
// rather than attempting an unbounded allocation on the strength of an
// attacker-supplied 32-bit length prefix.
const maxFrameFieldBytes = 8 << 20 // 8 MiB

// writeFrame emits the wire format {u32 len(root)}{root}{u32 len(cmd)}{cmd},
// big-endian, trusted root first. The root is written by the trusted parent
// process; the command is attacker-controlled and may contain arbitrary
// bytes — both are carried as raw length-prefixed byte strings so no byte
// value requires escaping.
func writeFrame(w io.Writer, workspaceRoot, command string) error {
	if err := writeField(w, workspaceRoot); err != nil {
		return fmt.Errorf("sandbox: write workspace root: %w", err)
	}
	if err := writeField(w, command); err != nil {
		return fmt.Errorf("sandbox: write command: %w", err)
	}
	return nil
}

// readFrame reads exactly the bytes written by writeFrame. A short or
// truncated stream, or a length prefix exceeding maxFrameFieldBytes, is a
// hard error — readFrame never returns a partially decoded root/command
// pair.
func readFrame(r io.Reader) (workspaceRoot, command string, err error) {
	workspaceRoot, err = readField(r)
	if err != nil {
		return "", "", fmt.Errorf("sandbox: read workspace root: %w", err)
	}

	command, err = readField(r)
	if err != nil {
		return "", "", fmt.Errorf("sandbox: read command: %w", err)
	}

	return workspaceRoot, command, nil
}

// writeField writes a single {u32 len}{bytes} field.
func writeField(w io.Writer, s string) error {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(s)))

	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	if _, err := io.WriteString(w, s); err != nil {
		return err
	}
	return nil
}

// readField reads a single {u32 len}{bytes} field written by writeField. It
// rejects a length claim over maxFrameFieldBytes before allocating, and
// requires the exact number of payload bytes claimed be present — a short
// read is an error, never a partial field.
func readField(r io.Reader) (string, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return "", fmt.Errorf("read length prefix: %w", err)
	}

	n := binary.BigEndian.Uint32(lenBuf[:])
	if n > maxFrameFieldBytes {
		return "", fmt.Errorf("length claim %d exceeds cap %d", n, maxFrameFieldBytes)
	}

	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return "", fmt.Errorf("read %d payload bytes: %w", n, err)
	}

	return string(payload), nil
}
