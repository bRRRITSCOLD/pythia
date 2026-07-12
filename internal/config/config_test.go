package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bRRRITSCOLD/pythia/internal/config"
)

// envVars lists every env var Load reads.
var envVars = []string{
	"PYTHIA_OLLAMA_BASE_URL",
	"PYTHIA_OLLAMA_MODEL",
	"PYTHIA_WORKSPACE_ROOT",
	"PYTHIA_DB_PATH",
	"PYTHIA_BASH_TIMEOUT",
	"PYTHIA_MAX_READ_BYTES",
	"PYTHIA_MAX_BASH_OUTPUT_BYTES",
	"PYTHIA_MAX_ITERATIONS",
	"PYTHIA_SESSION_ID",
	"PYTHIA_BASH_SANDBOX",
}

// unsetAll unsets every config env var for the duration of the test and
// restores each var's prior value (or absence) on cleanup. t.Setenv can only
// set a value, never truly unset one, so tests that need to exercise
// defaulting for a genuinely-unset var must unset directly via os.Unsetenv.
//
// It also points XDG_STATE_HOME at a fresh per-test temp dir so that any
// test reaching defaultDBPath() (i.e. one that leaves PYTHIA_DB_PATH unset)
// provisions its default state dir under that temp dir instead of mutating
// the real $HOME/.local/state/pythia on the developer's machine or CI
// runner. t.TempDir() returns a distinct directory per call, so this never
// collides with a separately-set PYTHIA_WORKSPACE_ROOT temp dir, preserving
// the "default DB path is outside the workspace" invariant.
func unsetAll(t *testing.T) {
	t.Helper()
	for _, v := range envVars {
		prev, wasSet := os.LookupEnv(v)
		if err := os.Unsetenv(v); err != nil {
			t.Fatalf("os.Unsetenv(%q): %v", v, err)
		}
		t.Cleanup(func() {
			if wasSet {
				os.Setenv(v, prev)
			}
		})
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

func TestLoad_NoEnvSet_AppliesValidDefaults(t *testing.T) {
	unsetAll(t)
	t.Setenv("PYTHIA_WORKSPACE_ROOT", t.TempDir())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.OllamaBaseURL != "http://localhost:11434" {
		t.Errorf("OllamaBaseURL = %q, want default", cfg.OllamaBaseURL)
	}
	if cfg.OllamaModel != "qwen3.5" {
		t.Errorf("OllamaModel = %q, want default", cfg.OllamaModel)
	}
	if filepath.Base(cfg.DBPath) != "pythia.db" {
		t.Errorf("DBPath = %q, want a path ending in pythia.db", cfg.DBPath)
	}
	if !filepath.IsAbs(cfg.DBPath) {
		t.Errorf("DBPath = %q, want an absolute path", cfg.DBPath)
	}
	if cfg.BashSandbox != config.SandboxOn {
		t.Errorf("BashSandbox = %v, want SandboxOn", cfg.BashSandbox)
	}
	if cfg.BashTimeout != 30*time.Second {
		t.Errorf("BashTimeout = %v, want 30s", cfg.BashTimeout)
	}
	if cfg.MaxReadBytes != 1048576 {
		t.Errorf("MaxReadBytes = %d, want 1048576", cfg.MaxReadBytes)
	}
	if cfg.MaxBashOutputBytes != 1048576 {
		t.Errorf("MaxBashOutputBytes = %d, want 1048576", cfg.MaxBashOutputBytes)
	}
	if cfg.MaxIterations != 10 {
		t.Errorf("MaxIterations = %d, want 10", cfg.MaxIterations)
	}
	if cfg.SessionID != "" {
		t.Errorf("SessionID = %q, want empty", cfg.SessionID)
	}
}

func TestLoad_InvalidBashTimeout_ReturnsError(t *testing.T) {
	unsetAll(t)
	t.Setenv("PYTHIA_WORKSPACE_ROOT", t.TempDir())
	t.Setenv("PYTHIA_BASH_TIMEOUT", "not-a-duration")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load() error = nil, want error for invalid duration")
	}
}

// TestLoad_ZeroOrNegativeTuningKnob_ReturnsError locks in the security
// constraint from issue #77 (SR-3, SR-4a, SR-4b, SR-4c): every gt=0 tuning
// knob must be rejected at startup when set to zero or negative, so the
// values these limits protect can never be zero/negative.
func TestLoad_ZeroOrNegativeTuningKnob_ReturnsError(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		value string
	}{
		{"BashTimeoutZero", "PYTHIA_BASH_TIMEOUT", "0s"},
		{"BashTimeoutNegative", "PYTHIA_BASH_TIMEOUT", "-5s"},
		{"MaxReadBytesZero", "PYTHIA_MAX_READ_BYTES", "0"},
		{"MaxReadBytesNegative", "PYTHIA_MAX_READ_BYTES", "-1"},
		{"MaxBashOutputBytesZero", "PYTHIA_MAX_BASH_OUTPUT_BYTES", "0"},
		{"MaxBashOutputBytesNegative", "PYTHIA_MAX_BASH_OUTPUT_BYTES", "-1"},
		{"MaxIterationsZero", "PYTHIA_MAX_ITERATIONS", "0"},
		{"MaxIterationsNegative", "PYTHIA_MAX_ITERATIONS", "-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetAll(t)
			t.Setenv("PYTHIA_WORKSPACE_ROOT", t.TempDir())
			t.Setenv(tt.env, tt.value)

			if _, err := config.Load(); err == nil {
				t.Fatalf("Load() error = nil, want error for %s=%q", tt.env, tt.value)
			}
		})
	}
}

// TestLoad_BashSandboxUnset_DefaultsOn locks in SR-3a.11: the sandbox is on
// by default when PYTHIA_BASH_SANDBOX is unset.
func TestLoad_BashSandboxUnset_DefaultsOn(t *testing.T) {
	unsetAll(t)
	t.Setenv("PYTHIA_WORKSPACE_ROOT", t.TempDir())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.BashSandbox != config.SandboxOn {
		t.Errorf("BashSandbox = %v, want SandboxOn", cfg.BashSandbox)
	}
}

// TestLoad_BashSandboxOff_ParsesOff locks in SR-3a.11: only the exact token
// "off" disables the sandbox.
func TestLoad_BashSandboxOff_ParsesOff(t *testing.T) {
	unsetAll(t)
	t.Setenv("PYTHIA_WORKSPACE_ROOT", t.TempDir())
	t.Setenv("PYTHIA_BASH_SANDBOX", "off")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.BashSandbox != config.SandboxOff {
		t.Errorf("BashSandbox = %v, want SandboxOff", cfg.BashSandbox)
	}
}

// TestLoad_BashSandboxGarbage_FailsClosedToOn locks in SR-3a.11's fail-safe
// requirement: any value other than the exact token "off" (including
// unrecognized garbage) resolves to SandboxOn.
func TestLoad_BashSandboxGarbage_FailsClosedToOn(t *testing.T) {
	garbageValues := []string{"On", "OFF", "0", "false", "disabled", "off ", " off", "yes"}

	for _, v := range garbageValues {
		t.Run(v, func(t *testing.T) {
			unsetAll(t)
			t.Setenv("PYTHIA_WORKSPACE_ROOT", t.TempDir())
			t.Setenv("PYTHIA_BASH_SANDBOX", v)

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load() returned error: %v", err)
			}
			if cfg.BashSandbox != config.SandboxOn {
				t.Errorf("BashSandbox = %v for %q, want SandboxOn (fail-safe)", cfg.BashSandbox, v)
			}
		})
	}
}

// TestLoad_DefaultDBPath_IsOutsideWorkspace locks in SR-3a.13: the default
// DB path must not resolve inside the (sandboxed, writable) workspace root,
// so a sandboxed bash command cannot rm/tamper the session DB.
func TestLoad_DefaultDBPath_IsOutsideWorkspace(t *testing.T) {
	unsetAll(t)
	workspace := t.TempDir()
	t.Setenv("PYTHIA_WORKSPACE_ROOT", workspace)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	rel, err := filepath.Rel(cfg.WorkspaceRoot, cfg.DBPath)
	isOutside := err != nil || rel == ".." || (len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator))
	if !isOutside {
		t.Errorf("DBPath = %q resolves inside WorkspaceRoot %q", cfg.DBPath, cfg.WorkspaceRoot)
	}
}

// TestLoad_ExplicitDBPath_HonoredVerbatim locks in SR-3a.13: an operator
// override of PYTHIA_DB_PATH is honored exactly, even if it happens to sit
// inside the workspace (operator's explicit choice).
func TestLoad_ExplicitDBPath_HonoredVerbatim(t *testing.T) {
	unsetAll(t)
	t.Setenv("PYTHIA_WORKSPACE_ROOT", t.TempDir())
	t.Setenv("PYTHIA_DB_PATH", "/tmp/explicit-pythia.db")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.DBPath != "/tmp/explicit-pythia.db" {
		t.Errorf("DBPath = %q, want verbatim override", cfg.DBPath)
	}
}

func TestLoad_NonexistentWorkspaceRoot_FailsValidation(t *testing.T) {
	unsetAll(t)
	t.Setenv("PYTHIA_WORKSPACE_ROOT", "/this/path/does/not/exist/pythia-test")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load() error = nil, want error for nonexistent workspace root")
	}
}

func TestLoad_AllEnvSet_OverridesDefaults(t *testing.T) {
	unsetAll(t)
	dir := t.TempDir()

	t.Setenv("PYTHIA_OLLAMA_BASE_URL", "http://example.com:1234")
	t.Setenv("PYTHIA_OLLAMA_MODEL", "custom-model")
	t.Setenv("PYTHIA_WORKSPACE_ROOT", dir)
	t.Setenv("PYTHIA_DB_PATH", "/tmp/custom.db")
	t.Setenv("PYTHIA_BASH_TIMEOUT", "45s")
	t.Setenv("PYTHIA_MAX_READ_BYTES", "2048")
	t.Setenv("PYTHIA_MAX_BASH_OUTPUT_BYTES", "4096")
	t.Setenv("PYTHIA_MAX_ITERATIONS", "25")
	t.Setenv("PYTHIA_SESSION_ID", "session-abc")
	t.Setenv("PYTHIA_BASH_SANDBOX", "off")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.OllamaBaseURL != "http://example.com:1234" {
		t.Errorf("OllamaBaseURL = %q", cfg.OllamaBaseURL)
	}
	if cfg.OllamaModel != "custom-model" {
		t.Errorf("OllamaModel = %q", cfg.OllamaModel)
	}
	wantRoot, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("filepath.EvalSymlinks(%q): %v", dir, err)
	}
	if cfg.WorkspaceRoot != wantRoot {
		t.Errorf("WorkspaceRoot = %q, want %q (canonicalized)", cfg.WorkspaceRoot, wantRoot)
	}
	if cfg.DBPath != "/tmp/custom.db" {
		t.Errorf("DBPath = %q", cfg.DBPath)
	}
	if cfg.BashTimeout != 45*time.Second {
		t.Errorf("BashTimeout = %v, want 45s", cfg.BashTimeout)
	}
	if cfg.MaxReadBytes != 2048 {
		t.Errorf("MaxReadBytes = %d, want 2048", cfg.MaxReadBytes)
	}
	if cfg.MaxBashOutputBytes != 4096 {
		t.Errorf("MaxBashOutputBytes = %d, want 4096", cfg.MaxBashOutputBytes)
	}
	if cfg.MaxIterations != 25 {
		t.Errorf("MaxIterations = %d, want 25", cfg.MaxIterations)
	}
	if cfg.SessionID != "session-abc" {
		t.Errorf("SessionID = %q", cfg.SessionID)
	}
	if cfg.BashSandbox != config.SandboxOff {
		t.Errorf("BashSandbox = %v, want SandboxOff", cfg.BashSandbox)
	}
}
