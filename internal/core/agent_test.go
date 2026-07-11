package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/bRRRITSCOLD/pythia/internal/core"
)

// providerResponse scripts one Chat() call for scriptedProvider: either a
// setup error, or a sequence of stream events delivered on a buffered
// channel (already fully "sent" before Chat returns, since the fakes need
// no real concurrency to exercise the loop contract).
type providerResponse struct {
	setupErr error
	events   []core.StreamEvent
}

// scriptedProvider replays one providerResponse per Chat() call, in order.
// Tests size the script to the exact number of round-trips they expect;
// an extra call panics on out-of-range index, which is the point — it means
// the loop contract under test made one more provider call than expected.
type scriptedProvider struct {
	script []providerResponse
	calls  int
}

func (p *scriptedProvider) Chat(ctx context.Context, req core.ChatRequest) (<-chan core.StreamEvent, error) {
	resp := p.script[p.calls]
	p.calls++
	if resp.setupErr != nil {
		return nil, resp.setupErr
	}
	ch := make(chan core.StreamEvent, len(resp.events))
	for _, ev := range resp.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

// loopingProvider always returns the same single tool call, done, forever —
// it stands in for a model that never stops asking for tools, to drive the
// SR-4a max-iterations bound.
type loopingProvider struct {
	call core.ToolCall
}

func (p *loopingProvider) Chat(ctx context.Context, req core.ChatRequest) (<-chan core.StreamEvent, error) {
	ch := make(chan core.StreamEvent, 1)
	ch <- core.StreamEvent{Done: true, ToolCalls: []core.ToolCall{p.call}}
	close(ch)
	return ch, nil
}

// cancelAwareProvider streams TextDelta events indefinitely, honoring ctx
// cancellation on every send attempt (mirroring the real Provider contract:
// "ctx cancellation aborts the turn and closes the channel").
type cancelAwareProvider struct{}

func (cancelAwareProvider) Chat(ctx context.Context, req core.ChatRequest) (<-chan core.StreamEvent, error) {
	ch := make(chan core.StreamEvent)
	go func() {
		defer close(ch)
		for {
			select {
			case ch <- core.StreamEvent{TextDelta: "x"}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// erroringTool always fails Invoke with a fixed infra error, standing in for
// a tool whose execution mechanism (not its business logic) breaks.
type erroringTool struct {
	name string
	err  error
}

func (t erroringTool) Schema() core.ToolSchema {
	return core.ToolSchema{Name: t.name, Description: "errors", Parameters: json.RawMessage(`{}`)}
}

func (t erroringTool) Invoke(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	return nil, t.err
}

func mustCreateSession(t *testing.T, repo *fakeSessionRepository, id string) {
	t.Helper()
	if err := repo.CreateSession(context.Background(), core.Session{ID: id}); err != nil {
		t.Fatalf("CreateSession(%q) returned error: %v", id, err)
	}
}

func drain(ch <-chan core.AgentEvent) []core.AgentEvent {
	var events []core.AgentEvent
	for ev := range ch {
		events = append(events, ev)
	}
	return events
}

func TestAgent_Send_NoToolCalls_EmitsDeltasThenTurnComplete(t *testing.T) {
	repo := newFakeSessionRepository()
	mustCreateSession(t, repo, "s1")

	provider := &scriptedProvider{script: []providerResponse{
		{events: []core.StreamEvent{
			{TextDelta: "Hello, "},
			{TextDelta: "world"},
			{Done: true},
		}},
	}}

	agent := core.NewAgent(provider, fakeToolRegistry{tool: fakeTool{}}, repo)

	ch, err := agent.Send(context.Background(), "s1", "hi")
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	events := drain(ch)

	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d: %+v", len(events), events)
	}
	if events[0].Type != core.EventTextDelta || events[0].TextDelta != "Hello, " {
		t.Fatalf("unexpected event[0]: %+v", events[0])
	}
	if events[1].Type != core.EventTextDelta || events[1].TextDelta != "world" {
		t.Fatalf("unexpected event[1]: %+v", events[1])
	}
	if events[2].Type != core.EventTurnComplete {
		t.Fatalf("unexpected event[2]: %+v", events[2])
	}

	msgs, err := repo.Messages(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 persisted messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != core.RoleUser || msgs[0].Content != "hi" {
		t.Fatalf("unexpected user message: %+v", msgs[0])
	}
	if msgs[1].Role != core.RoleAssistant || msgs[1].Content != "Hello, world" {
		t.Fatalf("unexpected assistant message: %+v", msgs[1])
	}
}

func TestAgent_Send_OneToolCall_ExecutesThenReInvokesProviderToCompletion(t *testing.T) {
	repo := newFakeSessionRepository()
	mustCreateSession(t, repo, "s1")

	call := core.ToolCall{ID: "call-1", Name: "fake", Args: json.RawMessage(`{"a":1}`)}
	provider := &scriptedProvider{script: []providerResponse{
		{events: []core.StreamEvent{{Done: true, ToolCalls: []core.ToolCall{call}}}},
		{events: []core.StreamEvent{{TextDelta: "done"}, {Done: true}}},
	}}

	agent := core.NewAgent(provider, fakeToolRegistry{tool: fakeTool{}}, repo)

	ch, err := agent.Send(context.Background(), "s1", "hi")
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	events := drain(ch)

	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d: %+v", len(events), events)
	}
	if events[0].Type != core.EventToolCallStarted || events[0].ToolCall == nil || events[0].ToolCall.ID != "call-1" {
		t.Fatalf("unexpected event[0]: %+v", events[0])
	}
	if events[1].Type != core.EventToolCallFinished || string(events[1].ToolResult) != `{"a":1}` {
		t.Fatalf("unexpected event[1]: %+v", events[1])
	}
	if events[2].Type != core.EventTextDelta {
		t.Fatalf("unexpected event[2]: %+v", events[2])
	}
	if events[3].Type != core.EventTurnComplete {
		t.Fatalf("unexpected event[3]: %+v", events[3])
	}

	if provider.calls != 2 {
		t.Fatalf("expected provider to be called twice, got %d", provider.calls)
	}

	msgs, err := repo.Messages(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("expected 4 persisted messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[2].Role != core.RoleTool || msgs[2].ToolCallID != "call-1" || msgs[2].Content != `{"a":1}` {
		t.Fatalf("unexpected tool message: %+v", msgs[2])
	}
}

func TestAgent_Send_UnknownSession_ReturnsErrSessionNotFound(t *testing.T) {
	repo := newFakeSessionRepository()
	agent := core.NewAgent(&scriptedProvider{}, fakeToolRegistry{tool: fakeTool{}}, repo)

	ch, err := agent.Send(context.Background(), "missing", "hi")
	if !errors.Is(err, core.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
	if ch != nil {
		t.Fatalf("expected a nil channel alongside the synchronous error, got %v", ch)
	}
}

func TestAgent_Send_ProviderSetupError_EmitsEventErrorNoCrash(t *testing.T) {
	repo := newFakeSessionRepository()
	mustCreateSession(t, repo, "s1")

	setupErr := errors.New("provider down")
	provider := &scriptedProvider{script: []providerResponse{{setupErr: setupErr}}}
	agent := core.NewAgent(provider, fakeToolRegistry{tool: fakeTool{}}, repo)

	ch, err := agent.Send(context.Background(), "s1", "hi")
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	events := drain(ch)

	if len(events) != 1 || events[0].Type != core.EventError || !errors.Is(events[0].Err, setupErr) {
		t.Fatalf("expected a single EventError wrapping the setup error, got %+v", events)
	}
}

func TestAgent_Send_MidStreamErr_EmitsEventError(t *testing.T) {
	repo := newFakeSessionRepository()
	mustCreateSession(t, repo, "s1")

	streamErr := errors.New("stream broke")
	provider := &scriptedProvider{script: []providerResponse{
		{events: []core.StreamEvent{{TextDelta: "partial"}, {Err: streamErr}}},
	}}
	agent := core.NewAgent(provider, fakeToolRegistry{tool: fakeTool{}}, repo)

	ch, err := agent.Send(context.Background(), "s1", "hi")
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	events := drain(ch)

	if len(events) != 2 {
		t.Fatalf("expected 2 events (delta then error), got %d: %+v", len(events), events)
	}
	if events[0].Type != core.EventTextDelta {
		t.Fatalf("unexpected event[0]: %+v", events[0])
	}
	if events[1].Type != core.EventError || !errors.Is(events[1].Err, streamErr) {
		t.Fatalf("unexpected event[1]: %+v", events[1])
	}
}

func TestAgent_Send_ModelLoopsForever_StopsAtMaxIterationsWithErr(t *testing.T) {
	repo := newFakeSessionRepository()
	mustCreateSession(t, repo, "s1")

	provider := &loopingProvider{call: core.ToolCall{ID: "loop", Name: "fake", Args: json.RawMessage(`{}`)}}
	agent := core.NewAgent(provider, fakeToolRegistry{tool: fakeTool{}}, repo, core.WithMaxIterations(3))

	ch, err := agent.Send(context.Background(), "s1", "hi")
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	events := drain(ch)

	if len(events) == 0 {
		t.Fatalf("expected at least one event")
	}
	last := events[len(events)-1]
	if last.Type != core.EventError || !errors.Is(last.Err, core.ErrMaxIterations) {
		t.Fatalf("expected the final event to be ErrMaxIterations, got %+v", last)
	}
}

func TestAgent_Send_ToolInfraError_EmitsEventError(t *testing.T) {
	repo := newFakeSessionRepository()
	mustCreateSession(t, repo, "s1")

	call := core.ToolCall{ID: "call-1", Name: "broken", Args: json.RawMessage(`{}`)}
	provider := &scriptedProvider{script: []providerResponse{
		{events: []core.StreamEvent{{Done: true, ToolCalls: []core.ToolCall{call}}}},
	}}

	toolErr := errors.New("infra failure")
	reg := fakeToolRegistry{tool: erroringTool{name: "broken", err: toolErr}}
	agent := core.NewAgent(provider, reg, repo)

	ch, err := agent.Send(context.Background(), "s1", "hi")
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	events := drain(ch)

	if len(events) != 2 {
		t.Fatalf("expected 2 events (started then error), got %d: %+v", len(events), events)
	}
	if events[0].Type != core.EventToolCallStarted {
		t.Fatalf("unexpected event[0]: %+v", events[0])
	}
	if events[1].Type != core.EventError || !errors.Is(events[1].Err, toolErr) {
		t.Fatalf("unexpected event[1]: %+v", events[1])
	}
}

func TestAgent_Send_UnknownTool_ReturnsErrorEnvelopeToModel(t *testing.T) {
	repo := newFakeSessionRepository()
	mustCreateSession(t, repo, "s1")

	call := core.ToolCall{ID: "call-1", Name: "missing-tool", Args: json.RawMessage(`{}`)}
	provider := &scriptedProvider{script: []providerResponse{
		{events: []core.StreamEvent{{Done: true, ToolCalls: []core.ToolCall{call}}}},
		{events: []core.StreamEvent{{Done: true}}},
	}}

	agent := core.NewAgent(provider, fakeToolRegistry{tool: fakeTool{}}, repo)

	ch, err := agent.Send(context.Background(), "s1", "hi")
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	events := drain(ch)

	if len(events) != 3 {
		t.Fatalf("expected 3 events (started, finished, turn-complete), got %d: %+v", len(events), events)
	}
	if events[1].Type != core.EventToolCallFinished {
		t.Fatalf("unexpected event[1]: %+v", events[1])
	}
	want := `{"error":"unknown tool missing-tool"}`
	if string(events[1].ToolResult) != want {
		t.Fatalf("expected envelope %s, got %s", want, events[1].ToolResult)
	}

	msgs, err := repo.Messages(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("expected 4 persisted messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[2].Role != core.RoleTool || msgs[2].Content != want {
		t.Fatalf("expected persisted tool message content %s, got %+v", want, msgs[2])
	}
}

func TestAgent_Send_CtxCancelledMidStream_StopsAndClosesChannel(t *testing.T) {
	repo := newFakeSessionRepository()
	mustCreateSession(t, repo, "s1")

	agent := core.NewAgent(cancelAwareProvider{}, fakeToolRegistry{tool: fakeTool{}}, repo)

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := agent.Send(ctx, "s1", "hi")
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	got := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range ch {
			got++
			if got == 2 {
				cancel()
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the event channel to close after ctx cancellation")
	}

	if got == 0 {
		t.Fatalf("expected at least one event before cancellation stopped the loop")
	}
}
