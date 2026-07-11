package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bRRRITSCOLD/pythia/internal/core"
)

// NewProgram wraps a Model bound to agent and sessionID in a *tea.Program,
// applying opts in order. The caller is responsible for ensuring sessionID
// already exists (e.g. via SessionRepository.CreateSession) before calling
// Run on the returned program.
func NewProgram(agent *core.Agent, sessionID string, opts ...tea.ProgramOption) *tea.Program {
	return tea.NewProgram(NewModel(agent, sessionID), opts...)
}
