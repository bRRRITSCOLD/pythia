//go:build !linux

package sandbox

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestRun_NonLinux_FailsClosedWithErrUnsupported(t *testing.T) {
	exitCode, err := Run(context.Background(), Policy{}, "echo hi", io.Discard, io.Discard)

	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Run() err = %v, want ErrUnsupported", err)
	}
	if exitCode != -1 {
		t.Errorf("Run() exitCode = %d, want -1", exitCode)
	}
}

func TestRunChild_NonLinux_FailsClosedNonZeroExit(t *testing.T) {
	if code := RunChild(); code == 0 {
		t.Fatalf("RunChild() = 0, want non-zero on unsupported platform")
	}
}
