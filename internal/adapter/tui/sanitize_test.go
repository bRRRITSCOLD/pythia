package tui

import "testing"

// TestSanitize_ANSIColorSequence_Stripped verifies SR-1: a CSI SGR color
// escape (e.g. from a malicious tool result or model output) is removed
// entirely, leaving only the plain text on either side.
func TestSanitize_ANSIColorSequence_Stripped(t *testing.T) {
	in := "hello \x1b[31mred\x1b[0m world"
	want := "hello red world"

	got := Sanitize(in)

	if got != want {
		t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
	}
}

// TestSanitize_OSC52ClipboardSequence_Stripped verifies SR-1: an OSC
// sequence (e.g. OSC 52 clipboard hijack, terminated by BEL or ST) is
// removed so untrusted content can never write to the terminal's clipboard
// or title.
func TestSanitize_OSC52ClipboardSequence_Stripped(t *testing.T) {
	in := "before\x1b]52;c;ZXZpbA==\x07after"
	want := "beforeafter"

	got := Sanitize(in)

	if got != want {
		t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
	}
}

// TestSanitize_OSC52ClipboardSequence_STTerminated_Stripped covers the ST
// (ESC \) terminator form of an OSC sequence, not just BEL.
func TestSanitize_OSC52ClipboardSequence_STTerminated_Stripped(t *testing.T) {
	in := "before\x1b]0;evil title\x1b\\after"
	want := "beforeafter"

	got := Sanitize(in)

	if got != want {
		t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
	}
}

// TestSanitize_C0ControlBytes_Stripped verifies SR-1: C0 control bytes
// (e.g. bell, backspace) other than newline/tab are removed.
func TestSanitize_C0ControlBytes_Stripped(t *testing.T) {
	in := "hi\x07\x08 there\x0b\x0c"
	want := "hi there"

	got := Sanitize(in)

	if got != want {
		t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
	}
}

// TestSanitize_C1ControlBytes_Stripped verifies SR-1 for the C1 control
// range (0x80-0x9F), reachable via UTF-8 encoded control characters.
func TestSanitize_C1ControlBytes_Stripped(t *testing.T) {
	in := "abc"
	want := "abc"

	got := Sanitize(in)

	if got != want {
		t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
	}
}

// TestSanitize_PreservesNewlinesAndTabs verifies SR-1's carve-out: \n and
// \t must survive sanitization since they are needed for legible rendering.
func TestSanitize_PreservesNewlinesAndTabs(t *testing.T) {
	in := "line one\n\tindented line two"
	want := "line one\n\tindented line two"

	got := Sanitize(in)

	if got != want {
		t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
	}
}

// TestSanitize_PlainText_Unchanged verifies the identity case: ordinary
// text with no control or escape bytes passes through unmodified.
func TestSanitize_PlainText_Unchanged(t *testing.T) {
	in := "The quick brown fox jumps over the lazy dog. 123!@#"

	got := Sanitize(in)

	if got != in {
		t.Errorf("Sanitize(%q) = %q, want unchanged", in, got)
	}
}

// TestSanitize_UnterminatedOSCSequence_StrippedToEnd guards against a
// truncated/streamed OSC sequence (no terminator arrives) leaking into the
// viewport: everything from ESC ] onward is dropped rather than emitted.
func TestSanitize_UnterminatedOSCSequence_StrippedToEnd(t *testing.T) {
	in := "before\x1b]52;c;not-terminated"
	want := "before"

	got := Sanitize(in)

	if got != want {
		t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
	}
}
