// Package config reads Pythia's configuration from the environment, applies
// documented defaults, and validates the result once at startup — Config is
// the composition root's (cmd/pythia) single source of runtime settings. It
// is a leaf package: it imports only the standard library and
// go-playground/validator, and is imported by cmd (never by internal/core or
// internal/adapter/*), per the dependency rule in
// docs/adr/0004-module-package-layout-dependency-rule.md.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-playground/validator/v10"
)

// Config holds Pythia's fully-resolved, validated runtime settings.
type Config struct {
	OllamaBaseURL      string          `validate:"required,url"`
	OllamaModel        string          `validate:"required"`
	WorkspaceRoot      string          `validate:"required,dir"`
	DBPath             string          `validate:"required"`
	BashTimeout        time.Duration   `validate:"required,gt=0"`
	MaxReadBytes       int64           `validate:"required,gt=0"`
	MaxBashOutputBytes int64           `validate:"required,gt=0"`
	MaxIterations      int             `validate:"required,gt=0"`
	SessionID          string          // optional; empty => cmd creates a new session
	BashSandbox        BashSandboxMode // the only parent-env switch that can disable the bash sandbox (SR-3a.11, SR-5)
}

// BashSandboxMode selects whether the bash tool runs inside the OS sandbox.
// It is read once at startup from the parent process's environment — never
// from a tool argument or session state — so nothing a model or tool call
// produces can flip it (SR-3a.11, SR-5).
type BashSandboxMode string

const (
	// SandboxOn runs bash tool invocations inside the OS sandbox. This is
	// the fail-safe default: any unset or unrecognized value resolves here.
	SandboxOn BashSandboxMode = "on"
	// SandboxOff disables the OS sandbox. It is selected only by the exact
	// token "off" — the sole escape hatch, and only from the parent env.
	SandboxOff BashSandboxMode = "off"
)

// Env var names and their documented defaults.
const (
	envOllamaBaseURL      = "PYTHIA_OLLAMA_BASE_URL"
	envOllamaModel        = "PYTHIA_OLLAMA_MODEL"
	envWorkspaceRoot      = "PYTHIA_WORKSPACE_ROOT"
	envDBPath             = "PYTHIA_DB_PATH"
	envBashTimeout        = "PYTHIA_BASH_TIMEOUT"
	envMaxReadBytes       = "PYTHIA_MAX_READ_BYTES"
	envMaxBashOutputBytes = "PYTHIA_MAX_BASH_OUTPUT_BYTES"
	envMaxIterations      = "PYTHIA_MAX_ITERATIONS"
	envSessionID          = "PYTHIA_SESSION_ID"
	envBashSandbox        = "PYTHIA_BASH_SANDBOX"

	defaultOllamaBaseURL      = "http://localhost:11434"
	defaultOllamaModel        = "qwen3.5"
	defaultBashTimeout        = 30 * time.Second
	defaultMaxReadBytes       = 1048576
	defaultMaxBashOutputBytes = 1048576
	defaultMaxIterations      = 10

	// defaultDBDirName / defaultDBFileName make up the default session-DB
	// location, rooted outside the workspace (SR-3a.13): a sandboxed bash
	// command can only write inside WorkspaceRoot, so a DB living under the
	// user's XDG state dir is unreachable to it.
	defaultDBDirName  = "pythia"
	defaultDBFileName = "pythia.db"
)

// Load reads Config from the environment, applying documented defaults for
// any unset variable, then validates the result. It returns an error — never
// panics — for a malformed duration/int env var or a value that fails
// validation (e.g. a WorkspaceRoot that doesn't exist as a directory).
func Load() (Config, error) {
	workspaceRoot := os.Getenv(envWorkspaceRoot)
	if workspaceRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return Config{}, fmt.Errorf("config: resolve default workspace root: %w", err)
		}
		workspaceRoot = cwd
	}

	// Canonicalize WorkspaceRoot to an absolute, symlink-evaluated path
	// before validation. WorkspaceRoot is the containment root for
	// downstream path-traversal defense (SR-4); resolving it once here
	// keeps containment comparisons in adapters robust against relative
	// paths or symlinks.
	if resolved, err := canonicalizeDir(workspaceRoot); err == nil {
		workspaceRoot = resolved
	}
	// If resolution fails (e.g. the path doesn't exist), leave
	// workspaceRoot as-is and let the `dir` validator below report the
	// error — canonicalization is best-effort and must not mask or
	// replace validation.

	bashTimeout, err := parseDuration(envBashTimeout, defaultBashTimeout)
	if err != nil {
		return Config{}, err
	}

	maxReadBytes, err := parseInt64(envMaxReadBytes, defaultMaxReadBytes)
	if err != nil {
		return Config{}, err
	}

	maxBashOutputBytes, err := parseInt64(envMaxBashOutputBytes, defaultMaxBashOutputBytes)
	if err != nil {
		return Config{}, err
	}

	maxIterations, err := parseInt(envMaxIterations, defaultMaxIterations)
	if err != nil {
		return Config{}, err
	}

	dbPath := os.Getenv(envDBPath)
	if dbPath == "" {
		resolved, err := defaultDBPath()
		if err != nil {
			return Config{}, fmt.Errorf("config: resolve default DB path: %w", err)
		}
		dbPath = resolved
	}

	cfg := Config{
		OllamaBaseURL:      stringOrDefault(envOllamaBaseURL, defaultOllamaBaseURL),
		OllamaModel:        stringOrDefault(envOllamaModel, defaultOllamaModel),
		WorkspaceRoot:      workspaceRoot,
		DBPath:             dbPath,
		BashTimeout:        bashTimeout,
		MaxReadBytes:       maxReadBytes,
		MaxBashOutputBytes: maxBashOutputBytes,
		MaxIterations:      maxIterations,
		SessionID:          os.Getenv(envSessionID),
		BashSandbox:        parseBashSandbox(envBashSandbox),
	}

	if err := validator.New().Struct(cfg); err != nil {
		return Config{}, fmt.Errorf("config: invalid configuration: %w", err)
	}

	return cfg, nil
}

// parseBashSandbox resolves the BashSandboxMode from the named env var.
// Fail-safe by design (SR-3a.11): unset, or any value other than the exact
// token "off", resolves to SandboxOn. Only "off" disables the sandbox.
func parseBashSandbox(env string) BashSandboxMode {
	if os.Getenv(env) == string(SandboxOff) {
		return SandboxOff
	}
	return SandboxOn
}

// defaultDBPath resolves the default session-DB location outside the
// default workspace root (SR-3a.13): $XDG_STATE_HOME/pythia/pythia.db,
// falling back to $HOME/.local/state/pythia/pythia.db. The containing
// directory is created if absent so the DB adapter can open it directly.
func defaultDBPath() (string, error) {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		stateHome = filepath.Join(home, ".local", "state")
	}

	dir := filepath.Join(stateHome, defaultDBDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create state dir %q: %w", dir, err)
	}

	return filepath.Join(dir, defaultDBFileName), nil
}

// canonicalizeDir resolves dir to an absolute, symlink-evaluated path.
func canonicalizeDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func stringOrDefault(env, def string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return def
}

func parseDuration(env string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(env)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s=%q is not a valid duration: %w", env, v, err)
	}
	return d, nil
}

func parseInt64(env string, def int64) (int64, error) {
	v := os.Getenv(env)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("config: %s=%q is not a valid integer: %w", env, v, err)
	}
	return n, nil
}

func parseInt(env string, def int) (int, error) {
	v := os.Getenv(env)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s=%q is not a valid integer: %w", env, v, err)
	}
	return n, nil
}
