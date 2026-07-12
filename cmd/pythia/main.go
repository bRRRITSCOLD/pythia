// Command pythia is the composition root — the single place every adapter
// meets (docs/architecture/first-slice.md §1.3, issue #71). It loads Config,
// opens SQLite (+ migrates), builds the four tools + registry, constructs
// the (lazy) Ollama provider, resolves-or-creates the session, wires the
// Agent, and runs the Bubble Tea program.
//
// Dependency rule: this is the ONLY package that imports every adapter.
// No wiring logic lives in internal/core — core stays stdlib-only.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bRRRITSCOLD/pythia/internal/adapter/provider/ollama"
	"github.com/bRRRITSCOLD/pythia/internal/adapter/store/sqlite"
	"github.com/bRRRITSCOLD/pythia/internal/adapter/tool/bash"
	"github.com/bRRRITSCOLD/pythia/internal/adapter/tool/bash/sandbox"
	"github.com/bRRRITSCOLD/pythia/internal/adapter/tool/edit"
	"github.com/bRRRITSCOLD/pythia/internal/adapter/tool/read"
	"github.com/bRRRITSCOLD/pythia/internal/adapter/tool/registry"
	"github.com/bRRRITSCOLD/pythia/internal/adapter/tool/write"
	"github.com/bRRRITSCOLD/pythia/internal/adapter/tui"
	"github.com/bRRRITSCOLD/pythia/internal/config"
	"github.com/bRRRITSCOLD/pythia/internal/core"
)

func main() {
	// Reserved re-exec hook (ADR-0005 §3): when the bash sandbox spine
	// re-execs this same binary via /proc/self/exe, it does so with this
	// exact argv[1] marker. This must be the very first thing main does —
	// before config.Load, before the TUI — and must never fall through to
	// the normal startup path below (sandbox.RunChild never returns on
	// success; it execve's into /bin/bash).
	if len(os.Args) > 1 && os.Args[1] == sandbox.ChildSubcommand {
		os.Exit(sandbox.RunChild())
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pythia:", err)
		os.Exit(1)
	}

	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "pythia:", err)
		os.Exit(1)
	}
}

// run is main's testable entry point: it delegates all wiring to bootstrap
// and then drives the TUI event loop. Kept separate from bootstrap so wiring
// can be exercised in tests without a TTY (bootstrap never calls
// program.Run()).
func run(cfg config.Config) error {
	program, dep, err := bootstrap(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = dep.Close() }()

	if _, err := program.Run(); err != nil {
		return fmt.Errorf("pythia: run tui: %w", err)
	}
	return nil
}

// deps holds the resources bootstrap constructs that outlive wiring and must
// be released by the caller (currently just the SQLite handle), plus the
// resolved session id the caller/tests need without re-deriving it.
type deps struct {
	Repo      *sqlite.Repo
	SessionID string
}

// Close releases every resource deps owns. Safe to call on a zero deps.
func (d *deps) Close() error {
	if d == nil || d.Repo == nil {
		return nil
	}
	return d.Repo.Close()
}

// bootstrap performs the full composition-root wiring in the exact startup
// order required by issue #71 / docs/architecture/first-slice.md
// (composition-root section, <200ms cold-start NFR, no network at startup):
//
//  1. sqlite.New            — opens the DB file and runs migrations.
//  2. build tools + registry — read/write/edit/bash behind registry.New.
//  3. ollama.New            — constructs the Provider; no network I/O yet
//     (the HTTP connection is established lazily on the first Agent.Send).
//  4. resolve-or-create the session (empty cfg.SessionID => NewID +
//     CreateSession; else GetSession, creating it if missing — resume path).
//  5. core.NewAgent(prov, reg, repo, WithMaxIterations(cfg.MaxIterations)).
//  6. tui.NewProgram(agent, sessionID).
//
// No message content is logged anywhere in this path (SR-7). On any error
// bootstrap releases whatever it already opened before returning.
func bootstrap(cfg config.Config) (*tea.Program, *deps, error) {
	repo, err := sqlite.New(cfg.DBPath)
	if err != nil {
		return nil, nil, fmt.Errorf("pythia: open sqlite %s: %w", cfg.DBPath, err)
	}

	tools := []core.Tool{
		read.New(cfg.WorkspaceRoot, cfg.MaxReadBytes),
		write.New(cfg.WorkspaceRoot),
		edit.New(cfg.WorkspaceRoot),
		bash.New(cfg.WorkspaceRoot, cfg.BashTimeout, cfg.MaxBashOutputBytes),
	}
	reg, err := registry.New(tools...)
	if err != nil {
		_ = repo.Close()
		return nil, nil, fmt.Errorf("pythia: build tool registry: %w", err)
	}

	// ollama.New performs no network I/O at construction time — the
	// connection is opened lazily on the first Chat call, satisfying the
	// no-network-at-startup NFR.
	prov := ollama.New(cfg.OllamaBaseURL, cfg.OllamaModel, nil)

	sessionID, err := ensureSession(context.Background(), repo, cfg.SessionID)
	if err != nil {
		_ = repo.Close()
		return nil, nil, fmt.Errorf("pythia: resolve session: %w", err)
	}

	agent := core.NewAgent(prov, reg, repo, core.WithMaxIterations(cfg.MaxIterations))

	program := tui.NewProgram(agent, sessionID)

	return program, &deps{Repo: repo, SessionID: sessionID}, nil
}

// ensureSession resolves the session id to run against:
//   - an empty sessionID means "start fresh": a new id is generated
//     (core.NewID) and the session is created.
//   - a non-empty sessionID is looked up; if it already exists it is reused
//     as-is (the resume path — history replays via repo.Messages inside the
//     Agent's turn loop). If it does not exist, it is created with that
//     exact id, so a caller-supplied PYTHIA_SESSION_ID always resolves to a
//     usable session on first use.
func ensureSession(ctx context.Context, repo core.SessionRepository, sessionID string) (string, error) {
	if sessionID == "" {
		id := core.NewID()
		now := time.Now().UTC()
		if err := repo.CreateSession(ctx, core.Session{ID: id, CreatedAt: now, UpdatedAt: now}); err != nil {
			return "", err
		}
		return id, nil
	}

	if _, err := repo.GetSession(ctx, sessionID); err != nil {
		if !errors.Is(err, core.ErrSessionNotFound) {
			return "", err
		}
		now := time.Now().UTC()
		if err := repo.CreateSession(ctx, core.Session{ID: sessionID, CreatedAt: now, UpdatedAt: now}); err != nil {
			return "", err
		}
	}
	return sessionID, nil
}
