package toolkit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePath_RelativeInsideRoot_Resolves(t *testing.T) {
	root := t.TempDir()

	got, err := ResolvePath(root, "sub/file.txt")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(got, root) {
		t.Fatalf("%q not under %q", got, root)
	}
}

func TestResolvePath_DotDotEscape_Rejected(t *testing.T) {
	_, err := ResolvePath(t.TempDir(), "../../etc/passwd")

	if !errors.Is(err, ErrPathEscape) {
		t.Fatalf("want ErrPathEscape, got %v", err)
	}
}

func TestResolvePath_AbsolutePath_Rejected(t *testing.T) {
	_, err := ResolvePath(t.TempDir(), "/etc/passwd")

	if !errors.Is(err, ErrPathEscape) {
		t.Fatalf("want ErrPathEscape, got %v", err)
	}
}

func TestResolvePath_SymlinkEscapingRoot_Rejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	_, err := ResolvePath(root, "link/secret")

	if !errors.Is(err, ErrPathEscape) {
		t.Fatalf("symlink escape not caught: %v", err)
	}
}

func TestResolvePath_EmptyPath_Rejected(t *testing.T) {
	_, err := ResolvePath(t.TempDir(), "")

	if !errors.Is(err, ErrPathEscape) {
		t.Fatalf("want ErrPathEscape, got %v", err)
	}
}

func TestResolvePath_NestedNonExistentTarget_ResolvesUnderRoot(t *testing.T) {
	root := t.TempDir()

	got, err := ResolvePath(root, "new/nested/file.txt")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(got, root) {
		t.Fatalf("%q not under %q", got, root)
	}
}

func TestResolvePath_DotDotWithinRoot_Resolves(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}

	got, err := ResolvePath(root, "sub/../file.txt")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(got, root) {
		t.Fatalf("%q not under %q", got, root)
	}
}
