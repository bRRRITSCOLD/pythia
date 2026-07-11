package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bRRRITSCOLD/pythia/internal/core"
)

// statusStyle and errorStyle render the status line (Lip Gloss). Kept as
// package-level values rather than Model fields: they're stateless and
// shared by every Model instance, so there's nothing to gain from plumbing
// them through construction.
var (
	statusStyle = lipgloss.NewStyle().Faint(true)
	errorStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))
)

// agentChannelMsg carries the event channel returned by Agent.Send once the
// submit Cmd completes, so Update can start listening on it.
type agentChannelMsg struct {
	ch  <-chan core.AgentEvent
	err error
}

// agentEventMsg carries one core.AgentEvent read off the active channel,
// plus the channel itself so Update can re-subscribe for the next event
// (the "goroutine feeds a tea.Msg channel via tea.Cmd re-subscription"
// bridge described in the T15 spec). closed is true once the channel has
// been drained and closed by the Agent, signalling the turn ended without
// an explicit EventTurnComplete/EventError (defensive; the Agent contract
// always sends one of those before closing, but Update must stay correct
// even if that ever changes).
type agentEventMsg struct {
	event  core.AgentEvent
	ch     <-chan core.AgentEvent
	closed bool
}

// Model is the Bubble Tea model for the Pythia TUI. It depends only on
// core.Agent and core.AgentEvent — never on core.Provider or any adapter
// package — so the dependency-rule fitness test holds for the TUI too.
type Model struct {
	agent     *core.Agent
	sessionID string

	input    textarea.Model
	viewport viewport.Model
	// content is a *strings.Builder (not a value) because tea.Model's
	// Update/View methods have value receivers: Bubble Tea copies Model on
	// every dispatch. A strings.Builder value field would panic ("illegal
	// use of non-zero Builder copied by value") the moment it's written to
	// more than once across copies; a pointer field copies cleanly since
	// every copy still points at the same one Builder.
	content *strings.Builder // accumulated, already-sanitized transcript

	status string // current status line text
	busy   bool   // a turn is in flight; submit is disabled while true
	err    error  // last EventError, if any; TUI stays usable (NFR)

	ready bool // WindowSizeMsg received; viewport/input sized

	cancel context.CancelFunc // cancels the in-flight turn's context, if any
}

// NewModel builds a TUI Model bound to a (agent) and a specific sessionID.
// The session is expected to already exist (created by the caller before
// starting the program); Agent.Send fails fast on an unknown sessionID.
func NewModel(a *core.Agent, sessionID string) Model {
	ta := textarea.New()
	ta.Placeholder = "Send a message..."
	ta.Prompt = "> "
	ta.ShowLineNumbers = false
	ta.SetHeight(3)
	ta.Focus()

	vp := viewport.New(0, 0)

	return Model{
		agent:     a,
		sessionID: sessionID,
		input:     ta,
		viewport:  vp,
		content:   &strings.Builder{},
		status:    "ready",
	}
}

// Init starts the input cursor blinking; there is nothing to fetch before
// the first render.
func (m Model) Init() tea.Cmd {
	return textarea.Blink
}

// Update dispatches every tea.Msg the program delivers: keyboard/window
// events from Bubble Tea, and the agentChannelMsg/agentEventMsg pair that
// bridges Agent.Send's channel into the Bubble Tea event loop.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.resize(msg), nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case agentChannelMsg:
		if msg.err != nil {
			m.err = msg.err
			m.status = "error: " + Sanitize(msg.err.Error())
			m.busy = false
			if m.cancel != nil {
				m.cancel()
				m.cancel = nil
			}
			return m, nil
		}
		return m, listenForAgentEvent(msg.ch)

	case agentEventMsg:
		return m.handleAgentEvent(msg)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// View renders the viewport (transcript), status line, and input box.
func (m Model) View() string {
	if !m.ready {
		return "initializing..."
	}

	status := statusStyle.Render(m.status)
	if m.err != nil {
		status = errorStyle.Render("error: " + Sanitize(m.err.Error()))
	}

	return fmt.Sprintf("%s\n%s\n%s", m.viewport.View(), status, m.input.View())
}

// resize applies a tea.WindowSizeMsg to the viewport and input, reserving
// space for the status line and input box.
func (m Model) resize(msg tea.WindowSizeMsg) Model {
	inputHeight := m.input.Height()
	const statusHeight = 1

	m.viewport.Width = msg.Width
	m.viewport.Height = msg.Height - inputHeight - statusHeight
	if m.viewport.Height < 0 {
		m.viewport.Height = 0
	}
	m.input.SetWidth(msg.Width)
	m.ready = true
	return m
}

// handleKey processes a keypress: Enter submits (unless a turn is already
// in flight, or the input is blank), Ctrl+C/Esc quits, everything else is
// forwarded to the input textarea.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC, tea.KeyEsc:
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit

	case tea.KeyEnter:
		if m.busy {
			return m, nil
		}
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}
		return m.submit(text)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// submit starts a turn: it clears the input, marks the model busy, and
// returns a Cmd that calls Agent.Send and reports the resulting channel
// (or error) back as an agentChannelMsg.
func (m Model) submit(text string) (tea.Model, tea.Cmd) {
	m.input.Reset()
	m.busy = true
	m.err = nil
	m.status = "thinking..."

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	agent := m.agent
	sessionID := m.sessionID
	return m, func() tea.Msg {
		ch, err := agent.Send(ctx, sessionID, text)
		return agentChannelMsg{ch: ch, err: err}
	}
}

// listenForAgentEvent returns a Cmd that blocks on ch for exactly one
// event (or its closure) and reports it as an agentEventMsg. Update
// re-issues this Cmd after every event to keep draining the channel — the
// re-subscription bridge described in the T15 spec — until the channel
// closes.
func listenForAgentEvent(ch <-chan core.AgentEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return agentEventMsg{ch: ch, closed: true}
		}
		return agentEventMsg{event: ev, ch: ch}
	}
}

// handleAgentEvent applies one AgentEvent to the model's rendered state.
// Every piece of untrusted text — TextDelta and ToolResult — is passed
// through Sanitize before it is appended to the transcript (SR-1). An
// EventError is shown on the status line but does not stop the TUI: the
// user can keep typing and start a new turn (graceful Ollama-down NFR).
func (m Model) handleAgentEvent(msg agentEventMsg) (tea.Model, tea.Cmd) {
	if msg.closed {
		m.busy = false
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		return m, nil
	}

	switch msg.event.Type {
	case core.EventTextDelta:
		m.appendContent(Sanitize(msg.event.TextDelta))
		m.status = "thinking..."

	case core.EventToolCallStarted:
		name := "tool"
		if msg.event.ToolCall != nil {
			name = Sanitize(msg.event.ToolCall.Name)
		}
		m.status = "running tool: " + name

	case core.EventToolCallFinished:
		m.appendContent(Sanitize(formatToolResult(msg.event.ToolResult)))
		m.status = "thinking..."

	case core.EventTurnComplete:
		m.busy = false
		m.status = "ready"
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		return m, nil

	case core.EventError:
		m.busy = false
		m.err = msg.event.Err
		m.status = "error"
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		return m, nil
	}

	return m, listenForAgentEvent(msg.ch)
}

// appendContent adds already-sanitized text to the transcript and scrolls
// the viewport to the bottom so streamed deltas stay visible as they
// arrive.
func (m *Model) appendContent(s string) {
	m.content.WriteString(s)
	m.viewport.SetContent(m.content.String())
	m.viewport.GotoBottom()
}

// formatToolResult renders a tool's JSON result envelope as plain text for
// the transcript. Invalid JSON (defensive; the Agent always produces a
// valid envelope) falls back to the raw bytes rather than dropping the
// output.
func formatToolResult(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var pretty interface{}
	if err := json.Unmarshal(raw, &pretty); err != nil {
		return string(raw)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(pretty); err != nil {
		return string(raw)
	}
	return strings.TrimRight(buf.String(), "\n")
}
