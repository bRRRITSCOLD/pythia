package core

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// defaultMaxIterations is the SR-4a loop bound applied when NewAgent is not
// given WithMaxIterations.
const defaultMaxIterations = 10

// Agent runs the synchronous turn loop (docs/architecture/first-slice.md
// §2.5, spec decision 1). It depends only on the Provider, ToolRegistry, and
// SessionRepository ports — never on any internal/adapter package — so the
// dependency-rule fitness test (internal/arch) holds for this file too.
type Agent struct {
	provider      Provider
	registry      ToolRegistry
	repo          SessionRepository
	maxIterations int
}

// AgentOption configures an Agent at construction time.
type AgentOption func(*Agent)

// WithMaxIterations overrides the default tool-call loop bound (SR-4a). n
// must be a positive number of provider round-trips per Send; callers
// passing n <= 0 get the default instead of a hung or zero-iteration loop.
func WithMaxIterations(n int) AgentOption {
	return func(a *Agent) {
		if n > 0 {
			a.maxIterations = n
		}
	}
}

// NewAgent wires an Agent to its three ports. opts are applied in order, so
// a later option wins if the same knob is set twice.
func NewAgent(p Provider, reg ToolRegistry, repo SessionRepository, opts ...AgentOption) *Agent {
	a := &Agent{
		provider:      p,
		registry:      reg,
		repo:          repo,
		maxIterations: defaultMaxIterations,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Send drives one user turn to completion (or failure) and streams
// AgentEvents describing it. See the T6 loop contract for the exact
// behavior; in short:
//
//  1. An unknown sessionID fails synchronously (no channel): the caller
//     never has to select on a channel that will never receive anything.
//  2. Otherwise the user message is persisted and a goroutine drives the
//     provider/tool loop, emitting events on the returned channel until it
//     reaches EventTurnComplete or EventError, then closes the channel.
func (a *Agent) Send(ctx context.Context, sessionID, userInput string) (<-chan AgentEvent, error) {
	if _, err := a.repo.GetSession(ctx, sessionID); err != nil {
		return nil, err
	}

	out := make(chan AgentEvent)
	go a.run(ctx, sessionID, userInput, out)
	return out, nil
}

// run is the goroutine body behind Send. It always closes out exactly once,
// however it exits, so callers can safely range over the channel returned
// by Send.
func (a *Agent) run(ctx context.Context, sessionID, userInput string, out chan<- AgentEvent) {
	defer close(out)

	userMsg := Message{
		ID:        NewID(),
		SessionID: sessionID,
		Role:      RoleUser,
		Content:   userInput,
		CreatedAt: time.Now().UTC(),
	}
	if err := a.repo.AppendMessage(ctx, userMsg); err != nil {
		a.emit(ctx, out, AgentEvent{Type: EventError, Err: err})
		return
	}

	for i := 0; i < a.maxIterations; i++ {
		toolCalls, ok := a.turn(ctx, sessionID, out)
		if !ok {
			return
		}
		if len(toolCalls) == 0 {
			a.emit(ctx, out, AgentEvent{Type: EventTurnComplete})
			return
		}
		if !a.runToolCalls(ctx, sessionID, toolCalls, out) {
			return
		}
	}

	a.emit(ctx, out, AgentEvent{Type: EventError, Err: ErrMaxIterations})
}

// turn runs one provider round-trip: load history, call Chat, drain the
// stream emitting EventTextDelta as it goes, and persist the resulting
// assistant message. ok is false when the turn ended in an error (already
// emitted) or the caller's context was cancelled, and the loop must stop.
func (a *Agent) turn(ctx context.Context, sessionID string, out chan<- AgentEvent) (toolCalls []ToolCall, ok bool) {
	messages, err := a.repo.Messages(ctx, sessionID)
	if err != nil {
		a.emit(ctx, out, AgentEvent{Type: EventError, Err: err})
		return nil, false
	}

	stream, err := a.provider.Chat(ctx, ChatRequest{Messages: messages, Tools: a.registry.Schemas()})
	if err != nil {
		a.emit(ctx, out, AgentEvent{Type: EventError, Err: err})
		return nil, false
	}

	var text strings.Builder
	for ev := range stream {
		if ev.Err != nil {
			a.emit(ctx, out, AgentEvent{Type: EventError, Err: ev.Err})
			return nil, false
		}
		if ev.TextDelta != "" {
			text.WriteString(ev.TextDelta)
			if !a.emit(ctx, out, AgentEvent{Type: EventTextDelta, TextDelta: ev.TextDelta}) {
				return nil, false
			}
		}
		if ev.Done {
			toolCalls = ev.ToolCalls
			break
		}
	}

	assistantMsg := Message{
		ID:        NewID(),
		SessionID: sessionID,
		Role:      RoleAssistant,
		Content:   text.String(),
		ToolCalls: toolCalls,
		CreatedAt: time.Now().UTC(),
	}
	if err := a.repo.AppendMessage(ctx, assistantMsg); err != nil {
		a.emit(ctx, out, AgentEvent{Type: EventError, Err: err})
		return nil, false
	}

	return toolCalls, true
}

// runToolCalls executes each requested tool call in order, persisting a
// RoleTool message per result and emitting the started/finished event pair.
// It returns false the moment an infra error or context cancellation stops
// the loop, matching turn's ok convention.
func (a *Agent) runToolCalls(ctx context.Context, sessionID string, calls []ToolCall, out chan<- AgentEvent) bool {
	for _, call := range calls {
		call := call
		if !a.emit(ctx, out, AgentEvent{Type: EventToolCallStarted, ToolCall: &call}) {
			return false
		}

		var result json.RawMessage
		tool, found := a.registry.Get(call.Name)
		if !found {
			envelope, err := json.Marshal(map[string]string{"error": "unknown tool " + call.Name})
			if err != nil {
				// json.Marshal on a map[string]string cannot fail; guard anyway
				// rather than silently dropping the envelope.
				a.emit(ctx, out, AgentEvent{Type: EventError, Err: err})
				return false
			}
			result = envelope
		} else {
			res, err := tool.Invoke(ctx, call.Args)
			if err != nil {
				a.emit(ctx, out, AgentEvent{Type: EventError, Err: err})
				return false
			}
			result = res
		}

		toolMsg := Message{
			ID:         NewID(),
			SessionID:  sessionID,
			Role:       RoleTool,
			Content:    string(result),
			ToolCallID: call.ID,
			CreatedAt:  time.Now().UTC(),
		}
		if err := a.repo.AppendMessage(ctx, toolMsg); err != nil {
			a.emit(ctx, out, AgentEvent{Type: EventError, Err: err})
			return false
		}

		if !a.emit(ctx, out, AgentEvent{Type: EventToolCallFinished, ToolResult: result}) {
			return false
		}
	}
	return true
}

// emit sends ev on out, ctx-aware: it reports false (instead of blocking
// forever) the moment ctx is cancelled, so every caller can bail out of the
// loop immediately and let the deferred close(out) run.
func (a *Agent) emit(ctx context.Context, out chan<- AgentEvent, ev AgentEvent) bool {
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}
