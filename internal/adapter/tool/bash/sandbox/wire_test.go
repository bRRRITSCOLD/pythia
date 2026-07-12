package sandbox

import (
	"bytes"
	"strings"
	"testing"
)

// TestFrame_RoundTrip_PreservesArbitraryBytes asserts the attacker-controlled
// command bytes (and the trusted workspace root) survive a writeFrame/
// readFrame round trip byte-identical, including newlines, NUL bytes, shell
// metacharacters, and a 1 MiB payload (SR-3a.13 integrity).
func TestFrame_RoundTrip_PreservesArbitraryBytes(t *testing.T) {
	large := strings.Repeat("a", 1<<20) // 1 MiB

	cases := map[string]struct {
		root string
		cmd  string
	}{
		"empty":       {root: "", cmd: ""},
		"simple":      {root: "/workspace/root", cmd: "echo hello"},
		"newline":     {root: "/workspace/root", cmd: "echo hello\nrm -rf /\n"},
		"nul-byte":    {root: "/workspace/root", cmd: "echo \x00 hidden"},
		"metachars":   {root: "/workspace/root", cmd: "echo $(whoami); `id` && cat /etc/passwd | tee out > file 2>&1 & ; ; $HOME"},
		"1mib-cmd":    {root: "/workspace/root", cmd: large},
		"unicode":     {root: "/工作区/根", cmd: "echo 你好 🚀"},
		"root-binary": {root: "root\x00\nwith\x00binary", cmd: "cmd"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer

			if err := writeFrame(&buf, tc.root, tc.cmd); err != nil {
				t.Fatalf("writeFrame: unexpected error: %v", err)
			}

			gotRoot, gotCmd, err := readFrame(&buf)
			if err != nil {
				t.Fatalf("readFrame: unexpected error: %v", err)
			}

			if gotRoot != tc.root {
				t.Errorf("root mismatch: got %q want %q", gotRoot, tc.root)
			}
			if gotCmd != tc.cmd {
				t.Errorf("command mismatch: got len=%d want len=%d", len(gotCmd), len(tc.cmd))
			}

			if buf.Len() != 0 {
				t.Errorf("expected readFrame to consume exactly the framed bytes, %d bytes left over", buf.Len())
			}
		})
	}
}

// TestFrame_TruncatedStream_ErrorsNoPartialAccept asserts that any stream
// truncated short of a complete frame — whether mid-length-prefix or
// mid-payload — is a hard error, never a partial accept (SR-3a.13,
// adversarial: a desynchronised frame must not be silently tolerated).
func TestFrame_TruncatedStream_ErrorsNoPartialAccept(t *testing.T) {
	var full bytes.Buffer
	if err := writeFrame(&full, "/workspace/root", "echo hello world"); err != nil {
		t.Fatalf("writeFrame: unexpected error: %v", err)
	}
	complete := full.Bytes()

	// Truncate at every possible byte offset short of the full frame, plus
	// the empty stream. Every one of these must error, never partially
	// decode a root/command pair.
	for cut := 0; cut < len(complete); cut++ {
		truncated := bytes.NewReader(complete[:cut])
		root, cmd, err := readFrame(truncated)
		if err == nil {
			t.Fatalf("cut=%d: expected error on truncated stream, got root=%q cmd=%q", cut, root, cmd)
		}
	}
}

// TestFrame_AbsurdLengthClaim_Rejected asserts that a length prefix claiming
// an absurd payload size is rejected outright rather than triggering an
// unbounded allocation attempt (adversarial: attacker controls the length
// prefix framing the command bytes).
func TestFrame_AbsurdLengthClaim_Rejected(t *testing.T) {
	t.Run("root-length-absurd", func(t *testing.T) {
		var buf bytes.Buffer
		writeU32(&buf, 0xFFFFFFFF) // claims ~4 GiB root

		_, _, err := readFrame(&buf)
		if err == nil {
			t.Fatal("expected error for absurd root length claim, got nil")
		}
	})

	t.Run("command-length-absurd", func(t *testing.T) {
		var buf bytes.Buffer
		root := "/workspace/root"
		writeU32(&buf, uint32(len(root)))
		buf.WriteString(root)
		writeU32(&buf, 0xFFFFFFFF) // claims ~4 GiB command

		_, _, err := readFrame(&buf)
		if err == nil {
			t.Fatal("expected error for absurd command length claim, got nil")
		}
	})

	t.Run("just-over-cap-rejected", func(t *testing.T) {
		var buf bytes.Buffer
		root := "/workspace/root"
		writeU32(&buf, uint32(len(root)))
		buf.WriteString(root)
		writeU32(&buf, maxFrameFieldBytes+1)

		_, _, err := readFrame(&buf)
		if err == nil {
			t.Fatal("expected error for length claim just over the cap, got nil")
		}
	})
}

// writeU32 is a small test helper mirroring the wire's big-endian u32
// encoding, used to construct adversarial byte streams directly.
func writeU32(buf *bytes.Buffer, v uint32) {
	b := make([]byte, 4)
	b[0] = byte(v >> 24)
	b[1] = byte(v >> 16)
	b[2] = byte(v >> 8)
	b[3] = byte(v)
	buf.Write(b)
}
