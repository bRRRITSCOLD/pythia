// Package ollama implements core.Provider against a local Ollama server's
// streaming POST /api/chat endpoint (NDJSON, one JSON object per line).
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/bRRRITSCOLD/pythia/internal/core"
)

// Provider is a core.Provider backed by Ollama's /api/chat endpoint.
type Provider struct {
	baseURL string
	model   string
	hc      *http.Client
}

// New returns a Provider that talks to the Ollama server at baseURL using
// model for every Chat call. A nil hc falls back to http.DefaultClient.
func New(baseURL, model string, hc *http.Client) *Provider {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Provider{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		hc:      hc,
	}
}

// Chat implements core.Provider. It POSTs the translated request with
// "stream": true and returns a channel fed by a goroutine that decodes the
// NDJSON response body, one core.StreamEvent per line, until a terminal
// (Done or Err) event.
func (p *Provider) Chat(ctx context.Context, req core.ChatRequest) (<-chan core.StreamEvent, error) {
	body, err := json.Marshal(toWireRequest(p.model, req))
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: unreachable: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("ollama: unexpected status %d", resp.StatusCode)
	}

	ch := make(chan core.StreamEvent)
	go stream(ctx, resp.Body, ch)
	return ch, nil
}

// stream decodes body as NDJSON, translating each line to a core.StreamEvent
// and sending it on ch, until a terminal event fires or ctx is cancelled. It
// always closes ch and body before returning.
func stream(ctx context.Context, body io.ReadCloser, ch chan<- core.StreamEvent) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var wr wireResponse
		if err := json.Unmarshal(line, &wr); err != nil {
			send(ctx, ch, core.StreamEvent{Err: fmt.Errorf("ollama: decode stream line: %w", err)})
			return
		}

		if wr.Message.Content != "" {
			if !send(ctx, ch, core.StreamEvent{TextDelta: wr.Message.Content}) {
				return
			}
		}

		if wr.Done {
			send(ctx, ch, core.StreamEvent{Done: true, ToolCalls: fromWireToolCalls(wr.Message.ToolCalls)})
			return
		}
	}

	if err := scanner.Err(); err != nil {
		send(ctx, ch, core.StreamEvent{Err: fmt.Errorf("ollama: stream read: %w", err)})
		return
	}

	// The connection closed (EOF) before a terminal "done": true line
	// arrived — a mid-stream drop.
	send(ctx, ch, core.StreamEvent{Err: errors.New("ollama: connection closed before terminal event")})
}

// send delivers ev on ch, honoring ctx cancellation so a caller that has
// abandoned the channel can never deadlock the goroutine. It reports whether
// the event was actually delivered (false means ctx won the race).
func send(ctx context.Context, ch chan<- core.StreamEvent, ev core.StreamEvent) bool {
	select {
	case ch <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// toWireRequest translates a core.ChatRequest into the wire shape Ollama's
// /api/chat expects, always requesting a stream.
func toWireRequest(model string, req core.ChatRequest) wireRequest {
	messages := make([]wireMessage, len(req.Messages))
	for i, m := range req.Messages {
		messages[i] = wireMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			ToolCalls:  toWireToolCalls(m.ToolCalls),
			ToolCallID: m.ToolCallID,
		}
	}

	var tools []wireTool
	if len(req.Tools) > 0 {
		tools = make([]wireTool, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = wireTool{
				Type: "function",
				Function: wireToolDef{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.Parameters,
				},
			}
		}
	}

	return wireRequest{
		Model:    model,
		Messages: messages,
		Tools:    tools,
		Stream:   true,
	}
}

func toWireToolCalls(tcs []core.ToolCall) []wireToolCall {
	if len(tcs) == 0 {
		return nil
	}
	out := make([]wireToolCall, len(tcs))
	for i, tc := range tcs {
		out[i] = wireToolCall{
			ID:       tc.ID,
			Function: wireToolCallFunc{Name: tc.Name, Arguments: tc.Args},
		}
	}
	return out
}

// fromWireToolCalls translates the response side's tool calls into
// core.ToolCall, assigning a fresh core.NewID() when Ollama omits one.
func fromWireToolCalls(wtcs []wireToolCall) []core.ToolCall {
	if len(wtcs) == 0 {
		return nil
	}
	out := make([]core.ToolCall, len(wtcs))
	for i, wtc := range wtcs {
		id := wtc.ID
		if id == "" {
			id = core.NewID()
		}
		out[i] = core.ToolCall{ID: id, Name: wtc.Function.Name, Args: wtc.Function.Arguments}
	}
	return out
}
