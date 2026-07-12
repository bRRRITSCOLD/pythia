# Residual risk — bash tool OS sandbox (SR-3a.H2)

Status: accepted, load-bearing (documented-invariant form, per
`docs/superpowers/specs/bash-sandbox.md` §Residual risk)
Scope: what the Landlock + seccomp sandbox (ADR-0005) does **not** close.

This note exists because the sandbox closes every *syscall-level* escape
(filesystem writes outside the workspace, network egress, dangerous
syscalls) but there is one channel it structurally cannot close. That
channel is accepted as residual risk, not fixed, and this document is the
record of that decision.

## The channel: stdout egress

The bash tool returns a command's stdout/stderr to the model, and the model
speaks to a provider. A sandboxed command can still:

```
cat ~/.ssh/id_rsa
```

Landlock does **not** restrict reads (only writes are scoped to the
workspace root and `/tmp` — see the spec's Outcomes §1); the seccomp policy
denies `socket()` for every address family, so the command cannot exfiltrate
by opening a connection itself. But the command's stdout is captured by the
bash tool and handed back to the model as a tool result — and the model's
next action is to send that tool result to the provider. Reading a secret
and returning it through the tool-output channel is not a network syscall,
so no control in this sandbox touches it.

This is true of every command run through the tool, not an edge case: any
`cat`, `grep`, `env`, or similar of a readable secret path succeeds and its
output flows back through `Invoke`'s `output.Stdout` unfiltered.

## Why this is accepted, not fixed, this slice

Closing this channel would mean either:

- a **read-denylist** for known secret paths (`~/.ssh`, `~/.aws`,
  `~/.config/gh`, `/proc/*/environ`, ...) — necessarily incomplete, since it
  enumerates locations rather than denying the class of behavior, and a
  moving target as new credential stores appear; or
- **output scrubbing** — inspecting tool output for secret-shaped content
  before it reaches the model, a nontrivial detection problem with its own
  false-negative risk.

Both are real engineering efforts disproportionate to the actual exposure
*given one load-bearing assumption* (below). The spec's Out-of-scope section
and the threat model (`docs/security/bash-sandbox-threat-model.md`,
SR-3a.H2) both defer this as a hardening follow-up rather than a must-fix
for this slice.

## The load-bearing assumption: the provider is local (Ollama)

The mitigation this slice relies on instead is **not a control in the
sandbox** — it is an environmental invariant: **Pythia's provider is Ollama,
running locally.**

A secret read via `cat` and returned through stdout only becomes an
*exfiltration* once it leaves the machine. With a local Ollama provider,
the tool result never crosses a network boundary — it stays on the same
host that could already read the file directly. The sandbox's network
denial (no outbound `socket()`) closes the syscall-level exfil path; the
local-provider assumption closes the remaining stdout-egress path by making
"egress" a no-op — there is nowhere off-box for the secret to go via the
model/provider round-trip either.

**This assumption is load-bearing, not incidental.** It is the entire reason
SR-3a.H2 is safe to defer. Nothing in the sandbox itself enforces "the
provider is local" — that is a deployment fact, currently true because
`internal/config` defaults `PYTHIA_OLLAMA_BASE_URL` to
`http://localhost:11434` and Pythia ships with no other provider adapter.

## Reopen trigger

This residual-risk acceptance **must be revisited** — treat SR-3a.H2 as
active again, not deferred — the moment any of the following becomes true:

- a **remote or hosted provider** is added (a cloud LLM API, a
  network-reachable Ollama instance, or any provider adapter whose base URL
  is not loopback);
- **telemetry** is added that forwards tool output (or transcripts
  containing it) off-box;
- **log-shipping** is added that ships stdout/session records to a remote
  sink.

Any of these turns the stdout-egress channel back into a live exfiltration
path, and a read-denylist or output-scrubbing control (the two options
above) becomes a must-fix, not a deferred hardening item.

## Related deferred hardening items

Two other hardening follow-ups from the threat model are recorded as
deferred alongside this one — deliberately not built this slice (YAGNI, per
the spec's Out-of-scope section):

- **SR-3a.H1 — Resource limits (rlimits).** `RLIMIT_NPROC`, `RLIMIT_FSIZE`,
  `RLIMIT_AS` (and a pids cgroup) are not applied. A fork bomb, a large
  write, or a memory hog inside a sandboxed command is bounded only by the
  existing per-invocation timeout and the bounded output buffer
  (`maxOutputBytes`), not by OS resource limits. Revisit if DoS-shaped
  abuse from sandboxed commands is observed.
- **SR-3a.H3 — Observability of denied syscalls.** The seccomp filter does
  not set `SECCOMP_RET_LOG` on any monitored subset, so a denied/killed
  syscall (network attempt, lethal-set probe) is not separately logged or
  auditable beyond the command's own exit code/output. Revisit if bypass
  attempts need to be detected rather than merely blocked.

## References

- Spec: `docs/superpowers/specs/bash-sandbox.md` (§Residual risk, §Out of
  scope)
- Threat model: `docs/security/bash-sandbox-threat-model.md` (§4,
  SR-3a.H1–H3; §5 "R. Accepted residuals")
- ADR: `docs/adr/0005-bash-tool-os-sandbox.md`
- Sandbox package: `internal/adapter/tool/bash/sandbox/doc.go`
- Wiring: `internal/adapter/tool/bash/bash.go`
