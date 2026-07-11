// Package tui is the Bubble Tea adapter: it renders the core.Agent event
// stream in a terminal UI. It depends only on internal/core — never on
// Provider or any other adapter — so the dependency-rule fitness test holds.
package tui

import "strings"

const (
	esc = '\x1b' // C0 ESC: introduces CSI/OSC escape sequences
	bel = '\x07' // C0 BEL: one valid OSC terminator
	del = '\x7f' // C0 DEL: control byte, not printable
)

// Sanitize strips C0/C1 control bytes and ANSI/OSC terminal escape sequences
// from s, satisfying SR-1: untrusted model text and tool output must never
// be able to hijack the terminal (move the cursor, rewrite the title, write
// to the clipboard via OSC 52, etc.) when rendered in the TUI viewport.
// '\n' and '\t' are preserved so multi-line, indented output still reads
// legibly. Sanitize is applied at the TUI render boundary only — never in
// core or the tools — so the rest of the system can pass strings through
// verbatim.
func Sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]

		if r == esc {
			i = skipEscapeSequence(runes, i)
			continue
		}

		if isStrippedControl(r) {
			continue
		}

		b.WriteRune(r)
	}

	return b.String()
}

// isStrippedControl reports whether r is a control byte that must be
// dropped: C0 controls (0x00-0x1F) other than \n and \t, DEL (0x7F), and
// the C1 control range (0x80-0x9F, reachable as decoded UTF-8 runes).
func isStrippedControl(r rune) bool {
	if r == '\n' || r == '\t' {
		return false
	}
	if r >= 0x00 && r <= 0x1f {
		return true
	}
	if r == del {
		return true
	}
	if r >= 0x80 && r <= 0x9f {
		return true
	}
	return false
}

// skipEscapeSequence consumes an ANSI escape sequence starting at runes[i]
// (which must be ESC) and returns the index of its last consumed rune, so
// the caller's loop increment lands on the first rune after the sequence.
// It handles CSI (ESC '[' ... final byte in 0x40-0x7E) and OSC (ESC ']'
// ... terminated by BEL or ST == ESC '\\') forms; any other or truncated
// sequence is consumed to the end of the string so nothing leaks through.
func skipEscapeSequence(runes []rune, i int) int {
	if i+1 >= len(runes) {
		return i
	}

	switch runes[i+1] {
	case '[':
		return skipCSI(runes, i+2)
	case ']':
		return skipOSC(runes, i+2)
	default:
		// Any other ESC-introduced sequence (single-character or unknown):
		// drop the ESC and the one following byte, matching common
		// terminal escape-sequence shapes without over-claiming syntax we
		// don't need to support.
		return i + 1
	}
}

// skipCSI consumes a CSI sequence's parameter/intermediate bytes starting
// at j (just after "ESC [") through its final byte (0x40-0x7E inclusive),
// returning the index of that final byte. If no final byte is found, the
// sequence is consumed to the end of the string.
func skipCSI(runes []rune, j int) int {
	for ; j < len(runes); j++ {
		if runes[j] >= 0x40 && runes[j] <= 0x7e {
			return j
		}
	}
	return j - 1
}

// skipOSC consumes an OSC sequence starting at j (just after "ESC ]")
// through its terminator: BEL (0x07) or ST (ESC '\\'). It returns the
// index of the last consumed rune. An unterminated sequence is consumed to
// the end of the string, so a truncated/streamed OSC payload can never
// leak into the rendered output.
func skipOSC(runes []rune, j int) int {
	for ; j < len(runes); j++ {
		if runes[j] == bel {
			return j
		}
		if runes[j] == esc && j+1 < len(runes) && runes[j+1] == '\\' {
			return j + 1
		}
	}
	return j - 1
}
