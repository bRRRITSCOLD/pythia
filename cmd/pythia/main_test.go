package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bRRRITSCOLD/pythia/internal/config"
	"github.com/bRRRITSCOLD/pythia/internal/core"
)

// newTestConfig builds a valid Config rooted at a fresh t.TempDir() (real
// SQLite file, no TTY involved) with the given SessionID.
func newTestConfig(t *testing.T, sessionID string) config.Config {
	t.Helper()
	dir := t.TempDir()
	return config.Config{
		OllamaBaseURL:      "http://localhost:11434",
		OllamaModel:        "qwen3.5",
		WorkspaceRoot:      dir,
		DBPath:             filepath.Join(dir, "pythia.db"),
		BashTimeout:        5 * time.Second,
		MaxReadBytes:       1 << 20,
		MaxBashOutputBytes: 1 << 20,
		MaxIterations:      10,
		SessionID:          sessionID,
	}
}

// TestRun_WithTempConfig_WiresAdaptersAndEnsuresSession exercises the
// bootstrap seam (never program.Run(), so no TTY is required) and asserts
// every adapter wired correctly and the configured session now exists.
func TestRun_WithTempConfig_WiresAdaptersAndEnsuresSession(t *testing.T) {
	cfg := newTestConfig(t, "test-session")

	program, dep, err := bootstrap(cfg)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = dep.Close() })

	if program == nil {
		t.Fatal("bootstrap returned a nil *tea.Program")
	}
	if dep.SessionID != "test-session" {
		t.Fatalf("dep.SessionID = %q, want %q", dep.SessionID, "test-session")
	}

	if _, err := dep.Repo.GetSession(context.Background(), "test-session"); err != nil {
		t.Fatalf("session not created by bootstrap: %v", err)
	}
}

// TestBootstrap_MissingSessionID_CreatesNewSession asserts the empty-SessionID
// path: bootstrap must generate a fresh id (core.NewID) and persist a new
// session under it.
func TestBootstrap_MissingSessionID_CreatesNewSession(t *testing.T) {
	cfg := newTestConfig(t, "")

	_, dep, err := bootstrap(cfg)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = dep.Close() })

	if dep.SessionID == "" {
		t.Fatal("bootstrap did not assign a session id for empty cfg.SessionID")
	}

	got, err := dep.Repo.GetSession(context.Background(), dep.SessionID)
	if err != nil {
		t.Fatalf("GetSession(%q): %v", dep.SessionID, err)
	}
	if got.ID != dep.SessionID {
		t.Fatalf("got.ID = %q, want %q", got.ID, dep.SessionID)
	}
}

// TestBootstrap_ExistingSessionID_ReusesIt asserts the resume path: a second
// bootstrap against the same DB and SessionID must reuse the existing
// session (not recreate it) and its prior history must still be present.
func TestBootstrap_ExistingSessionID_ReusesIt(t *testing.T) {
	cfg := newTestConfig(t, "resume-me")

	_, dep1, err := bootstrap(cfg)
	if err != nil {
		t.Fatalf("bootstrap (create): %v", err)
	}

	seed := core.Message{
		ID: core.NewID(), SessionID: "resume-me", Role: core.RoleUser,
		Content: "hello", CreatedAt: time.Now().UTC(),
	}
	if err := dep1.Repo.AppendMessage(context.Background(), seed); err != nil {
		t.Fatalf("seed AppendMessage: %v", err)
	}
	if err := dep1.Close(); err != nil {
		t.Fatalf("close dep1: %v", err)
	}

	_, dep2, err := bootstrap(cfg)
	if err != nil {
		t.Fatalf("bootstrap (resume): %v", err)
	}
	t.Cleanup(func() { _ = dep2.Close() })

	if dep2.SessionID != "resume-me" {
		t.Fatalf("dep2.SessionID = %q, want %q", dep2.SessionID, "resume-me")
	}

	msgs, err := dep2.Repo.Messages(context.Background(), "resume-me")
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "hello" {
		t.Fatalf("history not preserved across resume bootstrap: %+v", msgs)
	}
}
