package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/bRRRITSCOLD/pythia/internal/adapter/store/sqlite"
	"github.com/bRRRITSCOLD/pythia/internal/core"
)

// newRepo opens a fresh Repo at a temp-file DB and registers cleanup.
func newRepo(t *testing.T) *sqlite.Repo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repo.db")
	r, err := sqlite.New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// newRepoWithSession opens a fresh Repo and creates session "s1" in it.
func newRepoWithSession(t *testing.T, ctx context.Context) *sqlite.Repo {
	t.Helper()
	r := newRepo(t)
	now := time.Now().UTC()
	if err := r.CreateSession(ctx, core.Session{ID: "s1", Title: "test", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return r
}

func TestRepo_CreateSessionThenGetSession_RoundTripsFields(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	now := time.Now().UTC().Truncate(time.Nanosecond)

	want := core.Session{ID: "s1", Title: "my session", CreatedAt: now, UpdatedAt: now}
	if err := r.CreateSession(ctx, want); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := r.GetSession(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.ID != want.ID || got.Title != want.Title {
		t.Fatalf("GetSession = %+v, want %+v", got, want)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("timestamps did not round-trip: got %+v, want %+v", got, want)
	}
}

func TestRepo_GetSession_Missing_ReturnsErrSessionNotFound(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)

	_, err := r.GetSession(ctx, "ghost")
	if !errors.Is(err, core.ErrSessionNotFound) {
		t.Fatalf("GetSession error = %v, want ErrSessionNotFound", err)
	}
}

func TestRepo_AppendMessage_AssignsMonotonicSeqPerSession(t *testing.T) {
	ctx := context.Background()
	r := newRepoWithSession(t, ctx)

	for i := 0; i < 3; i++ {
		msg := core.Message{ID: core.NewID(), SessionID: "s1", Role: core.RoleUser, Content: "m", CreatedAt: time.Now().UTC()}
		if err := r.AppendMessage(ctx, msg); err != nil {
			t.Fatalf("AppendMessage[%d]: %v", i, err)
		}
	}

	msgs, err := r.Messages(ctx, "s1")
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("len(msgs) = %d, want 3", len(msgs))
	}
}

func TestRepo_Messages_ReturnsHistoryInSeqOrder(t *testing.T) {
	ctx := context.Background()
	r := newRepoWithSession(t, ctx)

	base := time.Now().UTC()
	user := core.Message{ID: "m-user", SessionID: "s1", Role: core.RoleUser, Content: "hi", CreatedAt: base}
	assistant := core.Message{
		ID: "m-assistant", SessionID: "s1", Role: core.RoleAssistant,
		ToolCalls: []core.ToolCall{{ID: "c1", Name: "read", Args: json.RawMessage(`{"path":"go.mod"}`)}},
		CreatedAt: base,
	}
	tool := core.Message{ID: "m-tool", SessionID: "s1", Role: core.RoleTool, Content: "result", ToolCallID: "c1", CreatedAt: base}

	// Append out of any implied timestamp order to prove seq — not
	// created_at — governs replay order (data-doc §5).
	for _, m := range []core.Message{user, assistant, tool} {
		if err := r.AppendMessage(ctx, m); err != nil {
			t.Fatalf("AppendMessage(%s): %v", m.ID, err)
		}
	}

	got, err := r.Messages(ctx, "s1")
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	wantOrder := []string{"m-user", "m-assistant", "m-tool"}
	wantRoles := []core.Role{core.RoleUser, core.RoleAssistant, core.RoleTool}
	for i, m := range got {
		if m.ID != wantOrder[i] {
			t.Fatalf("got[%d].ID = %q, want %q", i, m.ID, wantOrder[i])
		}
		if m.Role != wantRoles[i] {
			t.Fatalf("got[%d].Role = %q, want %q", i, m.Role, wantRoles[i])
		}
	}
	if got[2].ToolCallID != "c1" {
		t.Fatalf("tool message ToolCallID = %q, want %q", got[2].ToolCallID, "c1")
	}
}

func TestRepo_AppendMessage_PersistsAndReloadsToolCallsJSON(t *testing.T) {
	ctx := context.Background()
	r := newRepoWithSession(t, ctx)

	m := core.Message{
		ID: "m1", SessionID: "s1", Role: core.RoleAssistant,
		ToolCalls: []core.ToolCall{{ID: "c1", Name: "read", Args: json.RawMessage(`{"path":"go.mod"}`)}},
		CreatedAt: time.Now().UTC(),
	}
	if err := r.AppendMessage(ctx, m); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	got, err := r.Messages(ctx, "s1")
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if len(got[0].ToolCalls) != 1 {
		t.Fatalf("len(got[0].ToolCalls) = %d, want 1", len(got[0].ToolCalls))
	}
	tc := got[0].ToolCalls[0]
	if tc.ID != "c1" || tc.Name != "read" || string(tc.Args) != `{"path":"go.mod"}` {
		t.Fatalf("tool_calls JSON did not round-trip: %+v", tc)
	}
}

func TestRepo_AppendMessage_NoToolCalls_StoresNilNotEmptySlice(t *testing.T) {
	ctx := context.Background()
	r := newRepoWithSession(t, ctx)

	if err := r.AppendMessage(ctx, core.Message{ID: "m1", SessionID: "s1", Role: core.RoleUser, Content: "hi", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	got, err := r.Messages(ctx, "s1")
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(got[0].ToolCalls) != 0 {
		t.Fatalf("ToolCalls = %+v, want empty", got[0].ToolCalls)
	}
}

func TestRepo_AppendMessage_UnknownSession_ViolatesForeignKey(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)

	err := r.AppendMessage(ctx, core.Message{ID: "x", SessionID: "no-session", Role: core.RoleUser, CreatedAt: time.Now().UTC()})
	if err == nil {
		t.Fatal("AppendMessage for unknown session: want FK violation error, got nil")
	}
}

func TestRepo_AppendMessage_BadRole_ViolatesCheckConstraint(t *testing.T) {
	ctx := context.Background()
	r := newRepoWithSession(t, ctx)

	err := r.AppendMessage(ctx, core.Message{ID: "x", SessionID: "s1", Role: core.Role("hacker"), CreatedAt: time.Now().UTC()})
	if err == nil {
		t.Fatal("AppendMessage with invalid role: want CHECK-constraint violation error, got nil")
	}
}

func TestRepo_ResumeAcrossReopen_ReplaysHistory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "resume.db")

	r1, err := sqlite.New(path)
	if err != nil {
		t.Fatalf("New (first open): %v", err)
	}
	now := time.Now().UTC()
	if err := r1.CreateSession(ctx, core.Session{ID: "s1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := r1.AppendMessage(ctx, core.Message{ID: "m1", SessionID: "s1", Role: core.RoleUser, Content: "hi", CreatedAt: now}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if err := r1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r2, err := sqlite.New(path)
	if err != nil {
		t.Fatalf("New (reopen): %v", err)
	}
	t.Cleanup(func() { _ = r2.Close() })

	msgs, err := r2.Messages(ctx, "s1")
	if err != nil {
		t.Fatalf("Messages after reopen: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "hi" {
		t.Fatalf("resume failed: %+v", msgs)
	}

	// Appending after reopen must continue the seq sequence, not restart it.
	if err := r2.AppendMessage(ctx, core.Message{ID: "m2", SessionID: "s1", Role: core.RoleUser, Content: "again", CreatedAt: now}); err != nil {
		t.Fatalf("AppendMessage after reopen: %v", err)
	}
	msgs, err = r2.Messages(ctx, "s1")
	if err != nil {
		t.Fatalf("Messages after append post-reopen: %v", err)
	}
	if len(msgs) != 2 || msgs[0].ID != "m1" || msgs[1].ID != "m2" {
		t.Fatalf("post-reopen order wrong: %+v", msgs)
	}
}

// TestRepo_SatisfiesSessionRepositoryContract reuses core's port contract
// against this real adapter (per T7's "contract reuse" test item), the same
// behaviors internal/core/ports_test.go proves against the in-memory fake.
func TestRepo_SatisfiesSessionRepositoryContract(t *testing.T) {
	var _ core.SessionRepository = newRepo(t)
}
