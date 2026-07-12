# bash-sandbox (SR-3a) — Delivery Tracking Ledger

Execution contract for the orchestrate/dispatch loop. Maps each plan task (T1–T10) to
its GitHub issue, wave, blockers, and status. **Source of truth for scope:**
`docs/superpowers/plans/bash-sandbox.md`. This file owns the live delivery *state*.

- **Repo:** `bRRRITSCOLD/pythia`
- **Milestone (epic):** `bash-sandbox` — #4 — https://github.com/bRRRITSCOLD/pythia/milestone/4
- **Owner (all tasks):** `backend-engineer` (label `owner:backend-engineer`)
- **Labels per task:** its `wave-N`, `owner:backend-engineer` (+ `foundations` for Wave 0; `security-sensitive` for the three security-architect-gated tasks T5/T6/T7; `documentation` for T10)
- **🔒 Security-architect review REQUIRED before merge:** **T5** (#101, re-exec integrity), **T6** (#102, Landlock policy), **T7** (#103, seccomp policy) — the entire security perimeter (threat model §1.1). Do not merge these on a staff-engineer review alone.

## T# → Issue map

| T# | Issue | Wave | Title | blockedBy (issue #) | blockedBy (T#) | Sec-review | Status |
|----|-------|------|-------|---------------------|----------------|-----------|--------|
| T1  | #97  | wave-0 | Config — `PYTHIA_BASH_SANDBOX` mode + relocate DB out of writable scope | — | — | — | open |
| T2  | #98  | wave-0 | Sandbox package skeleton + deps + `Policy` + fail-closed non-Linux stub | — | — | — | open |
| T3  | #99  | wave-1 | Length-prefixed wire framing (policy + command) | #98 | T2 | — | open |
| T4  | #100 | wave-1 | Env scrub — allowlist + fixed `PATH` + absolute `/bin/bash` | #98 | T2 | — | open |
| T5  | #101 | wave-2 | Re-exec spine — `/proc/self/exe` + `__bash-sandbox` hook + pipe + fd hygiene + `NO_NEW_PRIVS` | #99, #100 | T3, T4 | 🔒 YES | open |
| T6  | #102 | wave-3 | Landlock layer — strict ABI ≥ 2, fail-closed, write-scope workspace + `/tmp` | #101 | T5 | 🔒 YES | open |
| T7  | #103 | wave-3 | seccomp layer — allowlist/default-deny, arch-validated, socket + io_uring + memory-poke + lethal-set | #101 | T5 | 🔒 YES | open |
| T8  | #104 | wave-4 | Wire the sandbox into `bashTool.Invoke` + fail-closed + one-time unsandboxed log | #97, #102, #103 | T1, T6, T7 | — | open |
| T9  | #105 | wave-5 | Bypass-probe integration/e2e suite (through `Invoke` + registry) | #104 | T8 | — | open |
| T10 | #106 | wave-5 | Docs — README, `PYTHIA_BASH_SANDBOX`, residual-risk, deferred H1–H3 | #104 | T8 | — | open |

## Wave-ordered ready-list (dispatch order)

A wave becomes ready only when every issue in the prior waves it depends on is closed.
Tasks within a wave are parallel-safe (file-disjoint) unless noted.

### Wave 0 — READY NOW (no blockers)
- **#97 (T1)** — ready immediately (no blockers). Off the critical path; only rejoins at T8.
- **#98 (T2)** — ready immediately (no blockers). *Critical: unblocks T3 + T4.*
- Both are parallel-safe (disjoint files: `internal/config/*` vs `internal/adapter/tool/bash/sandbox/*` + `go.mod`).

> Strictly no-blocker at t=0: **#97 (T1)** and **#98 (T2)**.

### Wave 1 — after T2 (#98) closes
- **#99 (T3)** and **#100 (T4)** — both ready as soon as #98 closes. Parallel-safe (disjoint files: `wire.go` vs `env.go`).

### Wave 2 — after T3 (#99) + T4 (#100) close
- **#101 (T5)** — the spine, no parallel peer. **🔒 security-architect review before merge.** Single serialization point.

### Wave 3 — after T5 (#101) closes
- **#102 (T6)** and **#103 (T7)** — both ready as soon as #101 closes. **Genuinely parallel-safe** — T5 wrote `child_linux.go` once to call `applyLandlock`/`applySeccomp` as file-disjoint stubs; T6 fills only `landlock_linux.go`, T7 fills only `seccomp_linux.go`. **🔒 both need security-architect review before merge.**

### Wave 4 — after T1 (#97) + T6 (#102) + T7 (#103) close
- **#104 (T8)** — integration point, no parallel peer. First moment the tool routes through the (now complete) sandbox. Note it also re-picks up T1 (#97) here — so T1 must be closed by this point even though it landed back in Wave 0.

### Wave 5 — after T8 (#104) closes
- **#105 (T9)** and **#106 (T10)** — both ready as soon as #104 closes. Parallel-safe (test-only vs docs-only; disjoint files).

## Critical path

`T2 (#98) → {T3 (#99), T4 (#100)} → T5 (#101) → {T6 (#102), T7 (#103)} → T8 (#104) → {T9 (#105), T10 (#106)}`

**T1 (#97) is OFF the critical path** — it is parallel-safe with all of Wave 0–3 and only rejoins as a blocker of T8 (#104). The **spine T5 (#101) is the single serialization point**: everything funnels through it. Once it lands, the two LSM layers (T6/T7) parallelize because `child_linux.go` was written once (in T5) to call the two apply functions as file-disjoint stubs. **No production code ships a no-op sandbox**: `bashTool.Invoke` is not wired to the spine until T8, which is blocked by both T6 and T7 — so the first moment the tool routes through the sandbox, the sandbox is complete.

Prioritize **#98 → #99/#100 → #101 → #102/#103 → #104** to keep the critical path moving. Get **T1 (#97)** done anytime in parallel before Wave 4.

## SR coverage matrix (must-fix SR-3a.1–.14)

Every must-fix SR is closed by a named task and proven by a named test.

| SR | Requirement | Closed by | Proven by (representative test) |
|----|-------------|-----------|----------------------------|
| SR-3a.1  | Default-deny seccomp allowlist | T7 (#103) | `TestSeccomp_NormalCommand_StillRuns`, `TestSeccomp_UnknownSyscall_DefaultDeny` |
| SR-3a.2  | io_uring blocked | T7 (#103) +T9 (#105) | `TestSeccomp_IoUring_DeniedOrKilled`, `TestSandbox_IoUringNetworkOrOpen_DeniedViaInvoke` |
| SR-3a.3  | socket() denied all families | T7 (#103) +T9 (#105) | `TestSeccomp_SocketAllFamilies_Denied`, `TestSandbox_AFUnixAndNetlink_DeniedViaInvoke` |
| SR-3a.4  | Foreign-arch / x32 killed | T7 (#103) +T9 (#105) | `TestSeccomp_ForeignArchX32_Killed`, `TestSandbox_X32Syscall_KilledViaInvoke` |
| SR-3a.5  | Memory-poke syscalls denied | T7 (#103) +T9 (#105) | `TestSeccomp_MemoryPoke_Denied`, `TestSandbox_PtraceAndMount_DeniedViaInvoke` |
| SR-3a.6  | NO_NEW_PRIVS; setuid gains nothing | T5 (#101) | `TestRun_NoNewPrivs_SetuidBinaryGainsNothing` |
| SR-3a.7  | fd hygiene (only 0/1/2) | T5 (#101) +T9 (#105) | `TestRun_FdHygiene_OnlyStdioReachesChild`, `TestSandbox_InheritedFdProbe_NotPresentInChild` |
| SR-3a.8  | Landlock write-scope + strict ABI | T6 (#102) +T9 (#105) | `TestLandlock_WriteOutsideScope_DeniedEACCES`, `TestLandlock_HardlinkOutOfScopeThenWrite_Denied`, `TestLandlock_BelowMinABI_FailsClosed` |
| SR-3a.9  | Filter persists across execve, TSYNC | T7 (#103) | `TestSeccomp_FilterPersistsIntoBash_PostExecStillDenied` |
| SR-3a.10 | Fail-closed on unavailable | T2 (#98, non-Linux) + T5 (#101, setup-fail) + T8 (#104, Invoke) | `TestRun_NonLinux_FailsClosedWithErrUnsupported`, `TestRun_ChildSetupFails_FailsClosedCommandNotRun`, `TestBash_SandboxOnButUnavailable_ReturnsErrorEnvelopeCommandNotRun` |
| SR-3a.11 | Hatch parent-env-only; child no off-branch | T1 (#97, config) + T8 (#104, parent decision) | `TestLoad_BashSandboxOff_ParsesOff`, `TestBash_SandboxOff_EmitsOneTimeUnsandboxedLog`, `TestSandbox_EscapeHatchOff_ProbesNowSucceed` |
| SR-3a.12 | Env allowlist + PATH reset | T4 (#100) +T9 (#105) | `TestScrubEnv_DropsInjectorsKeepsAllowlist`, `TestSandbox_FakeBashCurlOnWritablePath_NotUsed` |
| SR-3a.13 | Re-exec integrity + framing + DB/binary out of scope | T3 (#99, frame) + T5 (#101, re-exec) + T1 (#97, DB) | `TestFrame_RoundTrip_PreservesArbitraryBytes`, `TestRun_CommandWithMetachars_DeliveredIntactNeverArgv`, `TestLoad_DefaultDBPath_IsOutsideWorkspace`, `TestSandbox_BinaryOverwriteAttempt_DeniedReExecIntegrityHeld`, `TestSandbox_SessionDBTamperAttempt_Denied` |
| SR-3a.14 | Lethal-set killed | T7 (#103) +T9 (#105) | `TestSeccomp_LethalSet_Killed`, `TestSandbox_PtraceAndMount_DeniedViaInvoke` |

**Hardening SRs (deferred, documented — not built):** SR-3a.H1 (rlimits) → deferred, noted in T10 (#106). SR-3a.H2 (stdout-egress) → documented invariant in T10 (#106, spec's elected form). SR-3a.H3 (seccomp LOG observability) → deferred, noted in T10 (#106). All match spec §Out of scope.

## Global invariants (every task's green step)

- **Dependency rule:** `internal/core` imports **only** the Go stdlib. All sandbox code + its three deps live under `internal/adapter/tool/bash/sandbox`; the only non-bash change is the one-line `cmd/pythia/main.go` hook (T5). `make arch-test` (`-count=1`, uncached) must stay green in every PR.
- **CGO-free:** `CGO_ENABLED=0 go build ./...` → one static binary; `make check-cgo` passes on every PR touching `go.mod` (T2 only).
- **Build-tag split:** `GOOS=linux go build ./...` and `GOOS=windows go build ./...` both compile at every task boundary; the non-Linux stub fails closed (`ErrUnsupported`).
- **Fail-closed default:** if the sandbox cannot be fully established, `Invoke` returns an error envelope and the command does not run. The only override is `PYTHIA_BASH_SANDBOX=off` (parent env only).

## Status legend
`open` · `in-progress` (label `in-progress`) · `blocked` · `needs-rework` (label `needs-rework`, failed sec/staff review) · `done` (issue closed)

Update the Status column and re-check wave-readiness after each merge. For T5/T6/T7, "done" additionally requires the 🔒 security-architect review to have passed.
