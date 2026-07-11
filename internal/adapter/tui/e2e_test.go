// e2e_test.go drives the real tui.Model (as built by tui.NewProgram/
// tui.NewModel) through github.com/charmbracelet/x/exp/teatest against a
// stub core.Provider, a temp-file SessionRepository (internal/adapter/store/
// sqlite), and the real "read" tool + a minimal in-process ToolRegistry.
// It is a black-box test (package tui_test): it only touches tui's exported
// surface (NewModel, NewProgram), matching how a real caller assembles the
// program.
//
// Step 1 of T17 (open question from the spec): the current teatest import
// path is github.com/charmbracelet/x/exp/teatest — confirmed by `go get
// github.com/charmbracelet/x/exp/teatest@latest`, which resolves and is
// recorded in the import below.
package tui_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	teatest "github.com/charmbracelet/x/exp/teatest"

	"github.com/bRRRITSCOLD/pythia/internal/adapter/store/sqlite"
	"github.com/bRRRITSCOLD/pythia/internal/adapter/tool/read"
	"github.com/bRRRITSCOLD/pythia/internal/adapter/tui"
	"github.com/bRRRITSCOLD/pythia/internal/core"
)

// -----------------------------------------------------------------------
// Test doubles: a scripted Provider (one providerResponse per Chat call)
// and a minimal in-process ToolRegistry. The registry wraps real tools
// (internal/adapter/tool/read) so the tool round-trip in
// TestTUI_UserPrompt_StreamsAnswerAndRoundsTripToolCall exercises real
// tool execution, not a fake. A dedicated ToolRegistry adapter is tracked
// separately; until it lands, this local double is the smallest thing
// that satisfies core.ToolRegistry for this e2e journey.
// -----------------------------------------------------------------------

// providerResponse scripts one Chat() call: either a setup error, or a
// sequence of stream events delivered on a pre-filled, already-closed
// channel (the fake needs no real concurrency to drive the TUI loop).
type providerResponse struct {
	setupErr error
	events   []core.StreamEvent
}

// scriptedProvider replays one providerResponse per Chat() call, in order.
type scriptedProvider struct {
	script []providerResponse
	calls  int
}

func (p *scriptedProvider) Chat(_ context.Context, _ core.ChatRequest) (<-chan core.StreamEvent, error) {
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

// registry is a minimal in-process core.ToolRegistry over a fixed set of
// real core.Tool implementations, keyed by their own advertised name.
type registry struct {
	tools map[string]core.Tool
}

func newRegistry(tools ...core.Tool) *registry {
	r := &registry{tools: make(map[string]core.Tool, len(tools))}
	for _, t := range tools {
		r.tools[t.Schema().Name] = t
	}
	return r
}

func (r *registry) Schemas() []core.ToolSchema {
	schemas := make([]core.ToolSchema, 0, len(r.tools))
	for _, t := range r.tools {
		schemas = append(schemas, t.Schema())
	}
	return schemas
}

func (r *registry) Get(name string) (core.Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// -----------------------------------------------------------------------
// Shared harness
// -----------------------------------------------------------------------

const testSessionID = "e2e-session"

// newTestAgent wires a *core.Agent to a temp-file SQLite SessionRepository
// (t.Cleanup'd), a scriptedProvider replaying script, and reg. It creates
// testSessionID up front so Agent.Send never fails on an unknown session.
func newTestAgent(t *testing.T, script []providerResponse, reg core.ToolRegistry) *core.Agent {
	t.Helper()
	ctx := context.Background()

	repoPath := filepath.Join(t.TempDir(), "e2e.db")
	repo, err := sqlite.New(repoPath)
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	now := time.Now().UTC()
	if err := repo.CreateSession(ctx, core.Session{ID: testSessionID, Title: "e2e", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	provider := &scriptedProvider{script: script}
	return core.NewAgent(provider, reg, repo)
}

// newTUITestModel builds a tui.Model via the real NewModel constructor,
// wraps it in teatest, and registers cleanup that quits the program and
// waits for it to finish so no goroutine leaks past the test.
func newTUITestModel(t *testing.T, agent *core.Agent) *teatest.TestModel {
	t.Helper()
	model := tui.NewModel(agent, testSessionID)
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(120, 40))
	t.Cleanup(func() {
		tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
		tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
	})
	return tm
}

// submit types text into the program's input and presses Enter, mirroring
// a real user driving the TUI.
func submit(tm *teatest.TestModel, text string) {
	tm.Type(text)
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
}

// outputCapture accumulates everything the program has ever written to its
// output stream. teatest.WaitFor drains the underlying reader as it polls,
// so a second WaitFor call for text that already arrived (and was already
// drained) alongside an earlier match would spin until timeout; capture
// sidesteps that by never discarding what it has read, so every waitFor
// call — and every NotContains assertion — sees the full transcript so far.
type outputCapture struct {
	tm  *teatest.TestModel
	buf bytes.Buffer
}

func newOutputCapture(tm *teatest.TestModel) *outputCapture {
	return &outputCapture{tm: tm}
}

// drain reads whatever is currently available without blocking.
func (c *outputCapture) drain() {
	tmp := make([]byte, 4096)
	for {
		n, err := c.tm.Output().Read(tmp)
		if n > 0 {
			c.buf.Write(tmp[:n])
		}
		if err != nil {
			return
		}
	}
}

// waitFor polls until the accumulated output contains want, failing the
// test after 3s if it never does.
func (c *outputCapture) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c.drain()
		if bytes.Contains(c.buf.Bytes(), []byte(want)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q in program output; got:\n%s", want, c.buf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// -----------------------------------------------------------------------
// TestTUI_UserPrompt_StreamsAnswerAndRoundsTripToolCall
// -----------------------------------------------------------------------

// TestTUI_UserPrompt_StreamsAnswerAndRoundsTripToolCall drives a full user
// journey against the real tea.Program: type a prompt, stream incremental
// text, round-trip a real tool call (read), and stream the final answer —
// asserting each stage renders on screen.
func TestTUI_UserPrompt_StreamsAnswerAndRoundsTripToolCall(t *testing.T) {
	workspace := t.TempDir()
	filePath := filepath.Join(workspace, "greeting.txt")
	if err := os.WriteFile(filePath, []byte("hello from disk"), 0o600); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}

	readArgs, err := json.Marshal(map[string]string{"path": "greeting.txt"})
	if err != nil {
		t.Fatalf("marshal read args: %v", err)
	}

	script := []providerResponse{
		{events: []core.StreamEvent{
			{TextDelta: "Let me check that file"},
			{Done: true, ToolCalls: []core.ToolCall{{ID: "call-1", Name: "read", Args: readArgs}}},
		}},
		{events: []core.StreamEvent{
			{TextDelta: "The file says: hello from disk"},
			{Done: true},
		}},
	}

	reg := newRegistry(read.New(workspace, 1<<20))
	agent := newTestAgent(t, script, reg)
	tm := newTUITestModel(t, agent)
	out := newOutputCapture(tm)

	submit(tm, "what does greeting.txt say?")

	out.waitFor(t, "Let me check that file")
	out.waitFor(t, "hello from disk") // real tool's JSON result rendered
	out.waitFor(t, "The file says: hello from disk")
}

// -----------------------------------------------------------------------
// TestTUI_ProviderEmitsEscapeSequence_RendersInert (SR-1 end-to-end)
// -----------------------------------------------------------------------

// TestTUI_ProviderEmitsEscapeSequence_RendersInert proves SR-1 holds at the
// full-program boundary, not just at the Sanitize unit: a provider-streamed
// OSC 52 clipboard-hijack sequence (and a CSI color sequence around plain
// text) must never reach the rendered screen, while the surrounding plain
// text still renders.
func TestTUI_ProviderEmitsEscapeSequence_RendersInert(t *testing.T) {
	// OSC 52 sets the system clipboard from a base64 payload; "c2VjcmV0" is
	// base64 for "secret" — proof it never leaks means this exact string is
	// entirely absent from the rendered screen, not just unescaped.
	const oscPayload = "\x1b]52;c;c2VjcmV0\x07"
	const csiPayload = "\x1b[31mhijacked\x1b[0m"

	script := []providerResponse{
		{events: []core.StreamEvent{
			{TextDelta: "before " + csiPayload + " " + oscPayload + " after"},
			{Done: true},
		}},
	}

	reg := newRegistry()
	agent := newTestAgent(t, script, reg)
	tm := newTUITestModel(t, agent)
	out := newOutputCapture(tm)

	submit(tm, "trigger it")

	out.waitFor(t, "before")
	out.waitFor(t, "hijacked")
	out.waitFor(t, "after")

	rendered := out.buf.Bytes()
	if bytes.Contains(rendered, []byte("c2VjcmV0")) {
		t.Errorf("rendered output leaked the OSC 52 payload: %q", rendered)
	}
	if bytes.Contains(rendered, []byte("\x1b]52")) {
		t.Errorf("rendered output contains a raw OSC 52 escape sequence: %q", rendered)
	}
	if bytes.Contains(rendered, []byte("\x1b[31mhijacked")) {
		t.Errorf("rendered output contains the raw CSI escape sequence unstripped: %q", rendered)
	}
}

// -----------------------------------------------------------------------
// TestTUI_OllamaDown_ShowsErrorStaysUsable
// -----------------------------------------------------------------------

// TestTUI_OllamaDown_ShowsErrorStaysUsable simulates the Provider being
// unreachable (e.g. Ollama down): the first turn's setup error must surface
// on screen without leaving the TUI unusable — a second turn submitted
// right after must still succeed (graceful-degrade NFR, end-to-end).
func TestTUI_OllamaDown_ShowsErrorStaysUsable(t *testing.T) {
	script := []providerResponse{
		{setupErr: errors.New("connection refused")},
		{events: []core.StreamEvent{
			{TextDelta: "back online"},
			{Done: true},
		}},
	}

	reg := newRegistry()
	agent := newTestAgent(t, script, reg)
	tm := newTUITestModel(t, agent)
	out := newOutputCapture(tm)

	submit(tm, "hello?")
	out.waitFor(t, "connection refused")

	// The TUI must still be usable: a second submit drives a second,
	// independent Agent.Send/Chat round-trip to completion.
	submit(tm, "try again")
	out.waitFor(t, "back online")
}
