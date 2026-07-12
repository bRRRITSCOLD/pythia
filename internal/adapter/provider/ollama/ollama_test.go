package ollama_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bRRRITSCOLD/pythia/internal/adapter/provider/ollama"
	"github.com/bRRRITSCOLD/pythia/internal/core"
)

// ndjsonHandler builds an http.HandlerFunc that writes each line verbatim
// (with a trailing "\n"), flushing after every write to simulate a real
// streamed response, then returns without further ceremony (the connection
// closes normally after the handler returns).
func ndjsonHandler(lines ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		for _, line := range lines {
			fmt.Fprintln(w, line)
			flusher.Flush()
		}
	}
}

func collect(t *testing.T, ch <-chan core.StreamEvent, timeout time.Duration) []core.StreamEvent {
	t.Helper()
	var events []core.StreamEvent
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-deadline:
			t.Fatalf("timed out waiting for stream events; collected so far: %+v", events)
		}
	}
}

func TestProvider_Chat_StreamsTextDeltasThenTerminalDone(t *testing.T) {
	srv := httptest.NewServer(ndjsonHandler(
		`{"message":{"role":"assistant","content":"Hel"},"done":false}`,
		`{"message":{"role":"assistant","content":"lo"},"done":false}`,
		`{"message":{"role":"assistant","content":""},"done":true}`,
	))
	defer srv.Close()

	p := ollama.New(srv.URL, "qwen3.5", srv.Client())
	ch, err := p.Chat(context.Background(), core.ChatRequest{
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v, want nil", err)
	}

	events := collect(t, ch, 5*time.Second)
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(events), events)
	}
	if events[0].TextDelta != "Hel" || events[0].Done || events[0].Err != nil {
		t.Errorf("event[0] = %+v, want TextDelta \"Hel\"", events[0])
	}
	if events[1].TextDelta != "lo" || events[1].Done || events[1].Err != nil {
		t.Errorf("event[1] = %+v, want TextDelta \"lo\"", events[1])
	}
	if !events[2].Done || events[2].Err != nil || len(events[2].ToolCalls) != 0 {
		t.Errorf("event[2] = %+v, want terminal Done with no tool calls", events[2])
	}
}

func TestProvider_Chat_ToolCallsInResponse_DeliveredOnTerminalEvent(t *testing.T) {
	srv := httptest.NewServer(ndjsonHandler(
		`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"read_file","arguments":{"path":"a.txt"}}}]},"done":true}`,
	))
	defer srv.Close()

	p := ollama.New(srv.URL, "qwen3.5", srv.Client())
	ch, err := p.Chat(context.Background(), core.ChatRequest{
		Messages: []core.Message{{Role: core.RoleUser, Content: "read a.txt"}},
		Tools: []core.ToolSchema{
			{Name: "read_file", Description: "reads a file", Parameters: json.RawMessage(`{}`)},
		},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v, want nil", err)
	}

	events := collect(t, ch, 5*time.Second)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 terminal event: %+v", len(events), events)
	}
	ev := events[0]
	if !ev.Done || ev.Err != nil {
		t.Fatalf("event = %+v, want terminal Done with no error", ev)
	}
	if len(ev.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1: %+v", len(ev.ToolCalls), ev.ToolCalls)
	}
	tc := ev.ToolCalls[0]
	if tc.Name != "read_file" {
		t.Errorf("tool call name = %q, want %q", tc.Name, "read_file")
	}
	if tc.ID == "" {
		t.Errorf("tool call ID is empty, want a NewID()-assigned id since Ollama omitted one")
	}
	var args map[string]any
	if err := json.Unmarshal(tc.Args, &args); err != nil {
		t.Fatalf("tool call args not valid JSON: %v", err)
	}
	if args["path"] != "a.txt" {
		t.Errorf("tool call args = %v, want path=a.txt", args)
	}
}

// TestProvider_Chat_ToolCallsStreamedBeforeTerminal_StillDelivered covers the
// reasoning-model shape (e.g. qwen3.5): the model streams its tool call in a
// NON-terminal chunk (done:false) and the terminal chunk (done:true) carries
// no tool_calls. The adapter must accumulate tool calls across chunks and
// deliver them on the terminal Done event — harvesting only from the done line
// drops the call and yields an empty, do-nothing turn.
func TestProvider_Chat_ToolCallsStreamedBeforeTerminal_StillDelivered(t *testing.T) {
	srv := httptest.NewServer(ndjsonHandler(
		`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"bash","arguments":{"command":"ls"}}}]},"done":false}`,
		`{"message":{"role":"assistant","content":""},"done":true}`,
	))
	defer srv.Close()

	p := ollama.New(srv.URL, "qwen3.5", srv.Client())
	ch, err := p.Chat(context.Background(), core.ChatRequest{
		Messages: []core.Message{{Role: core.RoleUser, Content: "run bash: ls"}},
		Tools: []core.ToolSchema{
			{Name: "bash", Description: "runs a command", Parameters: json.RawMessage(`{}`)},
		},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v, want nil", err)
	}

	events := collect(t, ch, 5*time.Second)
	if len(events) == 0 {
		t.Fatal("got 0 events, want a terminal Done carrying the streamed tool call")
	}
	last := events[len(events)-1]
	if !last.Done || last.Err != nil {
		t.Fatalf("last event = %+v, want terminal Done with no error", last)
	}
	if len(last.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls on Done, want 1 (the call streamed before the terminal line): %+v", len(last.ToolCalls), last.ToolCalls)
	}
	if last.ToolCalls[0].Name != "bash" {
		t.Errorf("tool call name = %q, want %q", last.ToolCalls[0].Name, "bash")
	}
}

func TestProvider_Chat_OllamaUnreachable_ReturnsSetupError(t *testing.T) {
	// Bind and immediately close a listener to get a port nothing is
	// listening on, guaranteeing a connection-refused dial error.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	p := ollama.New("http://"+addr, "qwen3.5", nil)
	ch, err := p.Chat(context.Background(), core.ChatRequest{
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("Chat() error = nil, want a setup error for an unreachable Ollama")
	}
	if ch != nil {
		t.Errorf("Chat() channel = %v, want nil on setup error", ch)
	}
}

func TestProvider_Chat_ConnectionDropsMidStream_EmitsStreamEventErr(t *testing.T) {
	srv := httptest.NewServer(ndjsonHandler(
		// One valid, non-terminal line, then the handler returns and the
		// server closes the connection without ever sending "done": true.
		`{"message":{"role":"assistant","content":"partial"},"done":false}`,
	))
	defer srv.Close()

	p := ollama.New(srv.URL, "qwen3.5", srv.Client())
	ch, err := p.Chat(context.Background(), core.ChatRequest{
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v, want nil (drop happens mid-stream, not at setup)", err)
	}

	events := collect(t, ch, 5*time.Second)
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (partial TextDelta + terminal Err): %+v", len(events), events)
	}
	if events[0].TextDelta != "partial" {
		t.Errorf("event[0] = %+v, want TextDelta \"partial\"", events[0])
	}
	if events[1].Err == nil {
		t.Errorf("event[1] = %+v, want a non-nil Err for the dropped connection", events[1])
	}
}

func TestProvider_Chat_CtxCancelled_AbortsAndClosesChannel(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"first"},"done":false}`)
		flusher.Flush()
		// Hold the connection open until the client (test) is done with it,
		// or the request's context is cancelled by the client disconnecting.
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)

	p := ollama.New(srv.URL, "qwen3.5", srv.Client())
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := p.Chat(ctx, core.ChatRequest{
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v, want nil", err)
	}

	// Wait for the first event, then cancel.
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before first event")
		}
		if ev.TextDelta != "first" {
			t.Fatalf("first event = %+v, want TextDelta \"first\"", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first event")
	}

	cancel()

	// Drain until the channel closes; must happen promptly after cancel.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // closed, as required
			}
		case <-deadline:
			t.Fatal("channel did not close within 5s of ctx cancellation")
		}
	}
}

func TestProvider_Chat_NonStreamingSingleChunk_SatisfiesPort(t *testing.T) {
	// Some deployments may collapse a whole reply into a single line that
	// carries both content and the terminal done flag. The port's contract
	// only requires a well-formed sequence: zero or more TextDelta events
	// followed by exactly one terminal event.
	srv := httptest.NewServer(ndjsonHandler(
		`{"message":{"role":"assistant","content":"hi there"},"done":true}`,
	))
	defer srv.Close()

	p := ollama.New(srv.URL, "qwen3.5", srv.Client())
	ch, err := p.Chat(context.Background(), core.ChatRequest{
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v, want nil", err)
	}

	events := collect(t, ch, 5*time.Second)
	if len(events) == 0 {
		t.Fatal("got 0 events, want at least a terminal Done")
	}
	last := events[len(events)-1]
	if !last.Done || last.Err != nil {
		t.Errorf("last event = %+v, want terminal Done with no error", last)
	}
	var sawText bool
	for _, ev := range events {
		if ev.TextDelta == "hi there" {
			sawText = true
		}
	}
	if !sawText {
		t.Errorf("events = %+v, want the content \"hi there\" delivered somewhere before/at the terminal event", events)
	}
}
