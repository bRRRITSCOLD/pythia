package config_test

import (
	"os"
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
}

// unsetAll unsets every config env var for the duration of the test and
// restores each var's prior value (or absence) on cleanup. t.Setenv can only
// set a value, never truly unset one, so tests that need to exercise
// defaulting for a genuinely-unset var must unset directly via os.Unsetenv.
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
	if cfg.DBPath != "./pythia.db" {
		t.Errorf("DBPath = %q, want default", cfg.DBPath)
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
	if cfg.WorkspaceRoot != dir {
		t.Errorf("WorkspaceRoot = %q, want %q", cfg.WorkspaceRoot, dir)
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
}
