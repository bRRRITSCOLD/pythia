package sandbox

import "strings"

// fixedPATH is the only PATH the confined child ever sees. It is never
// derived from the parent process's environment: a prior command run by the
// agent could have planted a fake bash/curl/etc. earlier in a writable PATH
// directory, and inheriting that PATH would let the confined child pick it
// up (threat model §2.5, SR-3a.12).
const fixedPATH = "/usr/bin:/bin"

// bashPath is the absolute path to the shell the sandbox execve's into.
// It is never resolved via PATH lookup, for the same reason fixedPATH is
// fixed rather than inherited.
const bashPath = "/bin/bash"

// allowedEnvKeys is the exact set of environment variables that may reach
// the confined command, beyond PATH (which is always forced to fixedPATH).
// Everything else — including known injectors like LD_PRELOAD, BASH_ENV,
// ENV, IFS, SHELLOPTS, PROMPT_COMMAND, PYTHIA_BASH_SANDBOX, and any parent
// secrets — is dropped by construction: it is simply never on this list.
var allowedEnvKeys = [...]string{"HOME", "TERM", "LANG"}

// scrubEnv reduces the parent process's environment (in os.Environ() form,
// "KEY=VALUE" strings) to an allowlisted subset safe to hand to the
// sandboxed child. PATH is always set to fixedPATH regardless of what (if
// anything) the parent had. HOME, TERM, and LANG are passed through only
// when present in the parent; every other key is dropped.
//
// parent is a plain []string rather than os.Environ() called internally so
// the parent environment is injectable for testing.
func scrubEnv(parent []string) []string {
	values := make(map[string]string, len(allowedEnvKeys))
	for _, kv := range parent {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		for _, allowed := range allowedEnvKeys {
			if key == allowed {
				values[key] = value
				break
			}
		}
	}

	scrubbed := make([]string, 0, len(allowedEnvKeys)+1)
	scrubbed = append(scrubbed, "PATH="+fixedPATH)
	for _, key := range allowedEnvKeys {
		if value, ok := values[key]; ok {
			scrubbed = append(scrubbed, key+"="+value)
		}
	}
	return scrubbed
}
