# Project Stack Profile
<!-- Read by the `compainy` plugin implementation skills; overrides their defaults.
     Discipline (TDD/DDD/pragmatic-SOLID/DRY-KISS, ports-and-adapters,
     test tiers, Subject_Scenario_Expectation naming) is invariant. -->

## Languages
- Go

## Frontend
- Framework:            Terminal UI (TUI) — Bubble Tea (charmbracelet/bubbletea), NOT web
- Component primitives: Bubbles (charmbracelet/bubbles) widgets + Lip Gloss (charmbracelet/lipgloss) styling
- Styling:             Lip Gloss
- Forms / validation:  Bubbles textinput/textarea + huh (charmbracelet/huh) for prompts
- URL / query state:   n/a (no browser)

## Backend
- Language(s):         Go
- HTTP framework:      none for now — this is a local-first TUI app, not an HTTP service. Add Gin only if a hosted API surface is introduced later.
- Validation:          go-playground/validator (adapter-boundary parsing of tool args / plugin payloads)
# Architecture is invariant: ports-and-adapters / DI (not a stack parameter)
# Agent core = 4 tools (read, write, bash, edit); everything else extends via plugins.

## Data
- Primary store(s):    SQLite (embedded) via modernc.org/sqlite (pure Go, no CGO — preserves single-binary distribution). Sessions, conversation history, plugin state.
- Cache:               none
- Search / vector:     chromem-go (embedded pure-Go vector DB) for agent memory / skill RAG. No server.

## Infra
- Target:              local-only (single self-contained binary). Distributed as one Go binary.
- IaC:                 none
- Local infra:         none required — all state is embedded (SQLite + chromem-go). No docker-compose.

## Testing
- Runner(s):           go test
- E2E:                 teatest (charmbracelet/x/exp/teatest) for TUI golden-frame / interaction tests. No Playwright (no browser).
# Test tiers are invariant: unit / integration / e2e (not a stack parameter)

## Notes / constraints
- Plugin/extension system: hashicorp/go-plugin (gRPC subprocess) from the start — process-isolated, language-agnostic plugins. This is the "hermes-complexity" path: plugins/skills/modules are out-of-process gRPC servers behind stable interfaces.
- LLM provider abstraction: define a `Provider` port day 1. Current impl = local Ollama (qwen3.5) via HTTP. Future impl = Codex (subscription). Never call Ollama directly from core — always through the port.
- Bash-tool isolation is an OS concern, NOT a language/wasm concern: sandbox via Linux landlock + seccomp (and/or container/gVisor), not wasm. Wasm was considered for plugin isolation and rejected in favor of go-plugin gRPC.
- Single-binary discipline: prefer pure-Go / CGO-free deps (hence modernc.org/sqlite, chromem-go) so `go build` yields one portable binary.
