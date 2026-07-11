package core_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bRRRITSCOLD/pythia/internal/core"
)

// fakeProvider is a trivial in-memory Provider used only to prove the
// interface is implementable with the frozen signature.
type fakeProvider struct{}

func (fakeProvider) Chat(ctx context.Context, req core.ChatRequest) (<-chan core.StreamEvent, error) {
	ch := make(chan core.StreamEvent, 1)
	ch <- core.StreamEvent{Done: true}
	close(ch)
	return ch, nil
}

// fakeTool is a trivial Tool that echoes its args back as its result.
type fakeTool struct{}

func (fakeTool) Schema() core.ToolSchema {
	return core.ToolSchema{Name: "fake", Description: "fake tool", Parameters: json.RawMessage(`{}`)}
}

func (fakeTool) Invoke(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	return args, nil
}

// fakeToolRegistry is a trivial single-entry ToolRegistry.
type fakeToolRegistry struct {
	tool core.Tool
}

func (r fakeToolRegistry) Schemas() []core.ToolSchema {
	return []core.ToolSchema{r.tool.Schema()}
}

func (r fakeToolRegistry) Get(name string) (core.Tool, bool) {
	if name != r.tool.Schema().Name {
		return nil, false
	}
	return r.tool, true
}

// fakeSessionRepository is a trivial in-memory SessionRepository.
type fakeSessionRepository struct {
	sessions map[string]core.Session
	messages map[string][]core.Message
}

func newFakeSessionRepository() *fakeSessionRepository {
	return &fakeSessionRepository{
		sessions: make(map[string]core.Session),
		messages: make(map[string][]core.Message),
	}
}

func (r *fakeSessionRepository) CreateSession(ctx context.Context, s core.Session) error {
	r.sessions[s.ID] = s
	return nil
}

func (r *fakeSessionRepository) GetSession(ctx context.Context, id string) (core.Session, error) {
	s, ok := r.sessions[id]
	if !ok {
		return core.Session{}, core.ErrSessionNotFound
	}
	return s, nil
}

func (r *fakeSessionRepository) AppendMessage(ctx context.Context, m core.Message) error {
	r.messages[m.SessionID] = append(r.messages[m.SessionID], m)
	return nil
}

func (r *fakeSessionRepository) Messages(ctx context.Context, sessionID string) ([]core.Message, error) {
	return r.messages[sessionID], nil
}

// TestPorts_Signatures_AreImplementable is a compile-time contract test: it
// assigns a trivial fake to each port variable. If any port's signature
// drifts from docs/architecture/first-slice.md §2.2-§2.4, this file fails to
// compile — that is the point of the test.
func TestPorts_Signatures_AreImplementable(t *testing.T) {
	var _ core.Provider = fakeProvider{}
	var _ core.Tool = fakeTool{}
	var _ core.ToolRegistry = fakeToolRegistry{tool: fakeTool{}}
	var _ core.SessionRepository = newFakeSessionRepository()

	ctx := context.Background()

	// Exercise the fakes minimally so the test also has runtime assertions,
	// not just a compile-time check.
	var p core.Provider = fakeProvider{}
	ch, err := p.Chat(ctx, core.ChatRequest{})
	if err != nil {
		t.Fatalf("fakeProvider.Chat returned error: %v", err)
	}
	ev := <-ch
	if !ev.Done {
		t.Fatalf("expected terminal event to have Done=true")
	}

	reg := fakeToolRegistry{tool: fakeTool{}}
	if len(reg.Schemas()) != 1 {
		t.Fatalf("expected 1 schema, got %d", len(reg.Schemas()))
	}
	if _, ok := reg.Get("missing"); ok {
		t.Fatalf("expected Get for unregistered tool to return ok=false")
	}
	tool, ok := reg.Get("fake")
	if !ok {
		t.Fatalf("expected Get for registered tool to return ok=true")
	}
	out, err := tool.Invoke(ctx, json.RawMessage(`{"a":1}`))
	if err != nil {
		t.Fatalf("fakeTool.Invoke returned error: %v", err)
	}
	if string(out) != `{"a":1}` {
		t.Fatalf("expected echoed args, got %s", out)
	}

	repo := newFakeSessionRepository()
	if _, err := repo.GetSession(ctx, "missing"); err != core.ErrSessionNotFound {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
	sess := core.Session{ID: "s1", Title: "test"}
	if err := repo.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	got, err := repo.GetSession(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if got.ID != sess.ID {
		t.Fatalf("expected session id %q, got %q", sess.ID, got.ID)
	}
	msg := core.Message{ID: "m1", SessionID: "s1", Role: core.RoleUser, Content: "hi"}
	if err := repo.AppendMessage(ctx, msg); err != nil {
		t.Fatalf("AppendMessage returned error: %v", err)
	}
	msgs, err := repo.Messages(ctx, "s1")
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(msgs) != 1 || msgs[0].ID != "m1" {
		t.Fatalf("expected 1 message with id m1, got %+v", msgs)
	}
}
