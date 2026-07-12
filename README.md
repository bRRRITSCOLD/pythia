# Pythia

Pythia is a terminal AI coding agent written in Go, built on
[Bubble Tea](https://github.com/charmbracelet/bubbletea), talking to a local
[Ollama](https://ollama.com) model. It follows a ports-and-adapters
(hexagonal) architecture: `internal/core` is stdlib-only domain logic;
everything that touches the OS, the network, or the terminal lives behind an
adapter (`internal/adapter/*`), wired together at the composition root
(`cmd/pythia`). See `docs/adr/` for the recorded architecture decisions.

## Requirements

- Go 1.25+
- A running local [Ollama](https://ollama.com) instance (default
  `http://localhost:11434`)
- Linux with kernel **5.13+** and Landlock ABI ≥ 2 — required for the bash
  tool's OS sandbox (see [Bash tool sandbox](#bash-tool-sandbox) below). On
  any other platform, or an older kernel, the bash tool fails closed by
  default.

## Build

```
make build      # CGO_ENABLED=0 go build ./...
```

Pythia ships as a single static, CGO-free binary (no external runtime
dependency).

## Test

```
make test       # arch-test (dependency-rule guard) + go test ./...
```

## Configuration

Pythia reads its configuration from the environment once at startup
(`internal/config`). Every variable is optional; unset values fall back to
the documented default.

| Env var | Default | Meaning |
|---|---|---|
| `PYTHIA_OLLAMA_BASE_URL` | `http://localhost:11434` | Base URL of the Ollama server. |
| `PYTHIA_OLLAMA_MODEL` | `qwen3.5` | Model name to request from Ollama. |
| `PYTHIA_WORKSPACE_ROOT` | current working directory | Root directory the agent is scoped to (file reads/writes and the bash tool's write scope are bound to this). |
| `PYTHIA_DB_PATH` | `$XDG_STATE_HOME/pythia/pythia.db` (or `$HOME/.local/state/pythia/pythia.db`) | Session-history database path. Deliberately defaults **outside** the workspace root so a sandboxed bash command — confined to writing inside the workspace — cannot tamper with session history. |
| `PYTHIA_BASH_TIMEOUT` | `30s` | Per-invocation timeout for the bash tool. |
| `PYTHIA_MAX_READ_BYTES` | `1048576` (1 MiB) | Cap on bytes read by the file-read tool. |
| `PYTHIA_MAX_BASH_OUTPUT_BYTES` | `1048576` (1 MiB) | Cap on combined stdout/stderr captured from the bash tool per invocation; output beyond the cap is truncated, not an error. |
| `PYTHIA_MAX_ITERATIONS` | `10` | Max tool-call iterations the turn loop will run before stopping. |
| `PYTHIA_SESSION_ID` | (new session) | Resume an existing session by ID instead of starting a new one. |
| `PYTHIA_BASH_SANDBOX` | `on` | See [Bash tool sandbox](#bash-tool-sandbox) below. |

## Bash tool sandbox

The `bash` built-in tool runs model-chosen shell commands. Because the model
picks the command from untrusted context (a prompt, a prior tool result, a
file it read), that command is treated as hostile input, not
developer-authored script — so by default it runs inside an OS-enforced
sandbox (Landlock + seccomp-bpf, applied via a self-re-exec spine; see
[ADR-0005](docs/adr/0005-bash-tool-os-sandbox.md) and the
[threat model](docs/security/bash-sandbox-threat-model.md)). Sandboxed, a
command may:

- **read** broadly (unrestricted at the filesystem-syscall level);
- **write** only inside the workspace root and `/tmp` — anywhere else fails
  with `EACCES`;
- make **no network connections**, of any address family (`AF_INET`,
  `AF_INET6`, `AF_UNIX`, `AF_NETLINK`, ...), and cannot route around that
  denial via io_uring;
- run under a seccomp **allowlist** (default-deny) — dangerous syscalls
  (`ptrace`, `process_vm_readv/writev`, `mount`, `pivot_root`, `kexec_load`,
  kernel-module and `bpf` syscalls, `unshare`/`setns`, reboot/swap, and any
  foreign-arch/x32 syscall) are killed, not merely denied;
- see only an allowlisted, minimal environment (`PATH` fixed to a constant,
  plus `HOME`/`TERM`/`LANG`) — no inherited secrets, no `LD_PRELOAD`.

### `PYTHIA_BASH_SANDBOX`

| Value | Behavior |
|---|---|
| unset, or anything other than exactly `off` | **Sandbox on** (default, fail-safe). |
| `off` | Sandbox **disabled** — the bash tool runs the command directly, unsandboxed. Debug-only; this is the single, explicit escape hatch, and it is read once from the parent process's own environment — nothing a model or tool argument produces can set it. Every time this path runs, it emits a one-time `"bash sandbox DISABLED"` warning log so the unsandboxed state is never silent. |

### Kernel requirement and fail-closed behavior

The sandbox requires **Linux, kernel 5.13+, with Landlock ABI ≥ 2**. This is
a strict minimum, not a best-effort degrade: an older ABI would leave a
hardlink-based escape from the write-scope policy open, so the sandbox
refuses rather than run under a weaker guarantee.

If the sandbox cannot be established — a non-Linux platform, an
unsupported/older kernel, or any other setup failure — the bash tool
**fails closed**: it returns an error result and the command is **never
run**. It does not silently fall back to running unsandboxed. The only way
to run bash commands unsandboxed is the explicit `PYTHIA_BASH_SANDBOX=off`
escape hatch above.

### Residual risk

The sandbox does not — cannot — close every risk. In particular, a
sandboxed command's stdout is still returned to the model, so reading a
secret file and returning it via tool output is not blocked by any control
above. This is accepted as residual risk under a load-bearing assumption
(the provider is local Ollama, so that output never leaves the machine) with
an explicit reopen trigger if a remote provider is ever added. See
[`docs/security/bash-sandbox-residual-risk.md`](docs/security/bash-sandbox-residual-risk.md)
for the full write-up, and that document for the two related items
(resource limits, denied-syscall observability) recorded as deferred
hardening follow-ups rather than built this slice.
