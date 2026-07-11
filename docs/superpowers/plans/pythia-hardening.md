# Pythia — Post-Slice HARDENING Milestone: Implementation Plan

**Date:** 2026-07-11
**Status:** Plan — no code written yet. Feeds `project-manager` (transcribe tasks → issues, waves →
epics) and the build engineers (`backend-engineer` for every task; p0-security tasks additionally
run the `security-architect` deep-audit gate). Author role: `lead-engineer`.

**Inputs (design contract, locked, not relitigated here):**
- `docs/superpowers/plans/pythia-vertical-slice.md` (format precedent + the shipped slice this
  milestone hardens; SR-1..6 owning tasks live there)
- `docs/superpowers/security/pythia-threat-model.md` (SR-1..17 — the requirements this milestone
  advances: SR-6's blocking-host-call residual, SR-8, SR-9, SR-13-adjacent input hardening, SR-17)
- `docs/superpowers/data/pythia-data-model.md` (§5 replay rule / in-doubt window, §6 transactional
  boundary, §7 taint — **treated as frozen: no schema changes, no new event types, no relaxing the
  `effect_result` CHECK invariant**)
- `docs/superpowers/architecture/pythia-architecture.md`, `docs/adr/*` (ADR-0001 concrete-crate
  deps, ADR-0002 resume = pure function of history, ADR-0005 provider seam)

**Milestone scope.** Five tracking issues, decomposed into single-concern PR-sized tasks:
- **#34** (p0-security) — SR-6 wall-clock watchdog: close the *blocking-host-call* class fuel can't
  reach.
- **#36** (p0-security) — two capability-host input-hardening items (pre-bounds-check alloc; guest
  byte echo in reason strings).
- **#38** (no label) — structured `tool_calls`/`tool_call_id` on `pythia_provider::Message`.
- **#39** (no label) — two kernel follow-ups: SR-8 denial-taint inheritance; SR-9 exactly-once for
  external effects.
- **#41** (no label) — three CLI items: `Config` env coverage; `build_kernel` skills param; render
  control-char stripping for tainted content.

---

## 0. Non-negotiable repo process constraints (every implementer PR)

Embedded here so the orchestrate loop enforces them per task, not by memory:

1. **Base on latest `origin/main` only.** Rebase onto the tip of `origin/main` immediately before
   opening or updating a PR — never onto another in-flight branch. Waves exist precisely so a task's
   dependencies are *already merged to main* before it starts.
2. **Clean tree before force-push.** After any rebase or conflict resolution: `git add -A`, then
   `git status` must show a clean working tree (no unmerged paths, no stray files) **before** the
   force-push. A force-push from a dirty/half-resolved tree is a process violation.
3. **Merge gates (all three green, no exceptions):**
   - `cargo fmt --all --check`
   - `cargo clippy --workspace --all-targets -- -D warnings`
   - `cargo test --workspace` — runs against `MockProvider`; every live-Ollama test stays
     `#[ignore]` and is never a merge gate.
4. **One task = one agent session = one squash-merge PR.** No task bundles a second concern "while
   we're in here." If a task uncovers adjacent work, it is filed as a new task, not scope-crept.
5. **p0-security tasks additionally pass the `security-architect` deep-audit gate** before merge (see
   §4 per-task `p0?` column). The `security-architect` reviews independently of this plan; this plan
   only flags *which* tasks require that gate.

---

## Two cross-cutting decisions this plan settles

These are the two build-level decisions the milestone cannot proceed coherently without. Settled
here so no implementer re-litigates them mid-build and the `staff-engineer` review knows they were
deliberate.

### Decision A — #34 wall-clock watchdog mechanism: **worker thread + bounded join (Option B)**, not epoch interruption (Option A)

**The threat, precisely.** Fuel meters *executed guest instructions* — it already terminates a guest
infinite loop (`limits.rs::FUEL_BUDGET`). The residual class #34 targets is different: **a blocking
host call escapes fuel entirely.** While the single kernel thread is parked inside a synchronous host
function (a future `net:*_send` doing a blocking socket read, a slow `fs` read on a stalled mount, a
`poll_oneoff`-shaped wait), *no wasm instruction is executing*, so fuel never decrements and the
kernel loop hangs indefinitely.

**Why not epoch interruption (Option A).** Epoch interruption traps at guest-code back-edges and
function entries — i.e. only while *wasm* is executing. A thread blocked inside a Rust host function
is not executing wasm, so the epoch deadline can pass and the trap will not fire until (if ever)
control returns to the guest. Epoch therefore closes the *guest-loop* class (which fuel already
closes) and **does not close the named blocking-host-call class at all.** `limits.rs`'s own module
doc already records that epoch-plus-ticker was considered and rejected for the single-threaded fuel
case; this decision extends that reasoning: epoch is the wrong tool for the one class #34 exists to
close.

**Why worker thread + bounded join (Option B).** Running the whole `execute()` body on a spawned
worker and having the kernel `recv_timeout` on its result is the only mechanism of the two that
bounds *wall-clock* regardless of whether the runaway is in guest or host code. On timeout the kernel
returns `HostError::ResourceLimitExceeded` and proceeds; the worker is detached. Crucially, the
`Engine`/`Store`/`Instance` are all **created and destroyed on the worker thread** — only owned,
cloneable inputs (`module_bytes`, `manifest`, `policy`, `args`) cross into it and an already-`Send`
`ExecutionResult` crosses back — so there is **no `Send` bound to add to `Store`/`HostState`** and no
change to the single-threaded, deterministic kernel loop above `execute()`. ADR-0002 is untouched:
the kernel still calls `execute()` synchronously and journals its *result*; the threading is an
implementation detail of one function.

**Replay determinism (data-model §5/§7).** The wall-clock *timing* never reaches the log. The
timeout is recorded as `effect_result.status = "resource_limit_exceeded"` — the exact
outcome-not-timing shape the fuel/memory ceilings already produce (`dispatch.rs` maps
`ExecutionStatus::ResourceLimitExceeded` → that status string today). On resume, that `ToolResult` is
a recorded fact and is **never re-run** (data-model §5's replay rule), so the nondeterministic
wall-clock event is frozen the instant it commits and can never diverge a replay.

**Named residual.** An OS thread blocked in a syscall cannot be *killed* from Rust; on timeout the
worker is detached and leaks until the process exits. For a single-operator CLI running turns
sequentially this is bounded (≈0 in practice; a genuinely-hung skill is a bug, and process exit ends
it) and is the acceptable cost of actually unblocking the kernel — which is the stated requirement.
Fuel stays the first line for guest loops (cheaper, deterministic); the worker-join bound is the
class-closing backstop for everything fuel can't see.

### Decision B — #39 SR-9 exactly-once strategy: **deterministic dedup key (idempotency key)**, not two-phase intent→confirm journalling

**The gap.** `kernel/src/lib.rs::drive_turn` runs `dispatch_tool()` (which calls `execute()`, the
external effect) and *then* `append_event()` — two steps. A crash between them leaves the triggering
`LlmResponse{tool_call}` as the last event, so resume's `next_action` re-issues `DispatchTool` and the
effect runs **again**. Harmless for idempotent `read_file`; a double-send for a real non-idempotent
`net:*_send`.

**Why not two-phase intent→confirm journalling.** It is **structurally incompatible with the frozen
data model.** An "intent/pending" row is not a `ToolResult` (the CHECK constraint requires
`effect_result` non-null *iff* `type = 'ToolResult'`, and data-model §5 point 1 states "**No pending
`ToolResult` rows**" as a hard invariant). Journalling an intent would therefore require either a
sixth `events.type` value or relaxing that CHECK — both of which the data model deliberately forbids
(§4 "five values, deliberately not more"; §5 the immutability trigger is safe *because* nothing is
ever pending). And even if it were allowed, two-phase still cannot decide re-run-vs-skip after a
crash: it only detects "an intent exists without a confirm," not whether the external side effect
actually fired — that decision still needs a key at the *receiving* system. It pays a schema-breaking
cost for a partial answer.

**Why the deterministic dedup key.** Data-model §5 already names the correct mechanism verbatim:
"no event-sourced log closes [the in-doubt window] for genuinely non-idempotent external effects …
without an idempotency key at the receiving system." The kernel derives a **replay-stable** key from
data it already has in history — `turn_id` + the `seq` of the triggering `LlmResponse` + the tool name
+ a hash of the arguments — all pure functions of the log, so resume derives the identical key and a
retried effect is deduplicated *at the receiver*. This requires **no schema change, no new event
type**, and keeps resume a pure function of history (ADR-0002). Where a receiver cannot honor an
idempotency key (e.g. raw SMTP), the honest fallback is the manifest-declared skill-author
responsibility data-model §5 already assigns: the effect is marked non-idempotent and gated behind a
human-confirm (SR-9/SR-10 territory), never silently retried.

**Dependency call: SR-9 does *not* truly depend on #38.** #38 adds `tool_call_id` to the *provider
wire* `Message`. The dedup key is derived from the *kernel's own* `KernelEvent` history (`turn_id`,
`seq`, kernel `ToolCall.name`/`arguments`), which already exists independent of the provider wire
shape. A provider-assigned `tool_call_id` would be a *nicer* key source but is not required. #38 is a
prerequisite for the **strict-OpenAI-dialect net-send milestone in general**, not for the SR-9
mechanism specifically. Accordingly this plan sequences #39 independently of #38.

**Scope for *this* milestone.** No non-idempotent effect ships here (`read_file` is idempotent;
`net:*_send` is still the canned stub). So SR-9's task (H8) is a **design-decision + seam task**:
settle the dedup-key contract (above), define the manifest `effect_class`/`idempotent` declaration
seam in `pythia-manifest`, and land an `#[ignore]`d executable spec test that pins the contract —
*enforcement* (blocking an unconfirmed non-idempotent send) is explicitly deferred to the net-send
milestone that introduces the first such effect. H8 touches only docs + `pythia-manifest` (no
`dispatch.rs` edits), which keeps it file-disjoint from — and therefore parallel-safe with — the
SR-8 taint fix (H7).

---

## 1. Sequencing overview

**p0 first, per the handoff.** Waves 1–2 are the two p0-security parents (#34, #36). Waves 3–4 are
the remaining non-p0 work, arranged into two file-disjoint lanes that run concurrently:
- **Lane A (provider structured tool-calls):** #38a → {#38b, #38c}
- **Lane B (kernel taint + SR-9 design + CLI hardening):** #39a, #39b, #41a→#41b, #41c

Lane B is fully independent of Lane A (disjoint files), so both lanes open in Wave 3.

**Critical path (longest pole):** H2 → H3 → (milestone p0 complete), then H4 → {H5, H6} for Lane A.
The p0 capability-host lane is the structural bottleneck exactly as in the slice — every p0 item
funnels through `crates/capability-host/`.

---

## 2. Task list

Each task is one PR-sized, revertible unit. Tests are named before the approach is implemented (TDD):
write the named test, watch it fail for the right reason, then implement.

---

### H1 — #36 item 1: `read_memory` bounds-check before allocation

**Parent:** #36 (p0-security). **Closes:** #36 item 1 (Medium). **Wave:** 1. **p0-security: yes.**
**Crate:** `pythia-capability-host` (`src/lib.rs`).

**Goal:** `Instance::read_memory` must reject an out-of-bounds `len` **before** allocating, mirroring
the `host_fns::fs::read_guest_path` guard — closing the up-to-~2.1 GiB transient guest-controlled
allocation.

**Files:** `crates/capability-host/src/lib.rs` (`Instance::read_memory`, lines ~138–150).

**Approach:** Today `read_memory` does `vec![0u8; len as usize]` immediately after the negativity
check, then calls `memory.read`. Reorder to the `read_guest_path` pattern: fetch
`memory.data_size(&mut self.store)` first; reject (`HostError::Wasmtime`) when `len > memory_size`, or
when `offset.checked_add(len)` overflows or exceeds `memory_size`, **before** the `vec!` allocation.
Only allocate once the requested range is proven in-bounds. No behavior change for valid reads — this
is purely moving the ceiling check ahead of the allocation.

**Done when:**
- New test `ReadMemory_LenExceedingMemorySize_RejectedBeforeAllocating` — a `len` far larger than the
  instance's `data_size` returns `Err` and (asserted via the range/overflow guard path) does not
  allocate a `len`-sized buffer first.
- New test `ReadMemory_OffsetPlusLenOverflows_Rejected`.
- Existing `read_memory` happy-path tests (valid offset/len round-trips through `call_run`) still
  green.
- All three merge gates green.

**Owner:** backend-engineer (security-architect deep-audit gate — p0-security).
**Depends-on:** none (Wave 1 entry). Parallel-safe with H2.

---

### H2 — #36 item 2: sanitize/length-bound guest-controlled reason strings

**Parent:** #36 (p0-security). **Closes:** #36 item 2 (Low). **Wave:** 1. **p0-security: yes.**
**Crate:** `pythia-capability-host` (`src/execute.rs`).

**Goal:** Denial/Wasmtime reason strings must not echo unbounded guest-controlled bytes into a
public `ExecutionResult`. `CapabilityDenied(import)` carries `{module}::{name}` lifted verbatim from
the wasm import section, and `HostError::Wasmtime(err)` interpolates `{err}` — both flow into
`denied(...)`'s `ExecutionResult.bytes`, which the kernel then persists and renders.

**Files:** `crates/capability-host/src/execute.rs` (`result_for_host_error`, `denied`, +
a private sanitizer helper). (Sibling display in `lib.rs::HostError::fmt` may echo the same bytes;
leave `lib.rs` to H1/H3's owners — H2 sanitizes at the `ExecutionResult`-construction boundary only,
which is the one that persists/renders. Note the `lib.rs` `Display` as a follow-up comment, do not
edit it here.)

**Approach:** Add a private `sanitize_reason_fragment(&str) -> String` that (a) length-bounds the
guest-derived fragment (e.g. `MAX_REASON_FRAGMENT = 256` bytes, truncating with an explicit
`…(truncated)` marker) and (b) replaces control/non-printable characters (anything `< 0x20` except
none, plus `0x7f`, and ANSI CSI introducers) with a visible placeholder. Apply it to the
guest-derived `import` in the `CapabilityDenied` arm and to `{err}`'s rendered text in the `Wasmtime`
arm before they enter the reason string. The fixed prefixes ("capability denied: import …",
"execution failed: …") stay host-authored and unsanitized.

**Done when:**
- New test `Denied_GuestImportNameWithControlBytes_SanitizedInReason` — a `CapabilityDenied` whose
  import name contains `\x1b[2J`/`\n`/`\0` produces an `ExecutionResult` whose bytes contain none of
  those raw control bytes and contain the placeholder.
- New test `Denied_OversizedGuestReason_LengthBounded` — a reason fragment far over the cap is
  truncated with the marker.
- Existing `execute.rs` redaction/status tests still green.
- All three merge gates green.

**Owner:** backend-engineer (security-architect deep-audit gate — p0-security).
**Depends-on:** none (Wave 1 entry). Parallel-safe with H1 (disjoint files: `lib.rs` vs
`execute.rs`).

---

### H3 — #34: SR-6 wall-clock watchdog (worker-thread bounded execution)

**Parent:** #34 (p0-security). **Closes:** #34. **Wave:** 2. **p0-security: yes.**
**Crate:** `pythia-capability-host` (`src/execute.rs`, `src/limits.rs`). **Settles Decision A.**

**Goal:** Close the blocking-host-call class fuel cannot reach: bound every skill call by wall-clock,
force the kernel loop to proceed on exceedance, and record the kill as the deterministic
`resource_limit_exceeded` outcome (never timing) so replay stays deterministic.

**Files:** `crates/capability-host/src/execute.rs` (wrap instantiation+call in a bounded worker),
`crates/capability-host/src/limits.rs` (add `WALL_CLOCK_DEADLINE` constant alongside `FUEL_BUDGET`).

**Approach (per Decision A):**
- Add `pub(crate) const WALL_CLOCK_DEADLINE: Duration` to `limits.rs` (e.g. a few seconds — generous
  vs. real skill work, tight vs. a hang), documented the same way `FUEL_BUDGET`/`MEMORY_LIMIT_BYTES`
  are, and explicitly noting it is a *config constant*, so nothing timing-derived enters the log.
- In `execute()`: move the `CapabilityHost::new` → `instantiate` → `call_run` body onto a spawned
  worker thread. The worker owns the `Engine`/`Store`/`Instance` for its whole life (nothing wasmtime
  crosses the thread boundary — no `Send` bound is added to `HostState`/`Store`). Move only the owned
  inputs in (clone `module_bytes`/`manifest`/`policy`/`args` as needed) and send the finished
  `ExecutionResult` back over a `std::sync::mpsc` channel.
- The caller does `recv_timeout(WALL_CLOCK_DEADLINE)`. On `Ok` → return the result. On
  `RecvTimeoutError::Timeout` → detach the worker (drop the receiver / do not join) and return a
  `resource_limit_exceeded` `ExecutionResult` built via the same `resource_limit_exceeded(...)`
  constructor the fuel/memory path already uses (so `dispatch.rs`'s existing
  `ExecutionStatus::ResourceLimitExceeded` → `"resource_limit_exceeded"` mapping needs no change).
- Keep fuel + memory + table limits exactly as-is (first line for guest runaway); the worker-join is
  the backstop for what fuel can't see.

**Done when:**
- New test `WallClock_BlockingHostCall_ForceTerminatedAsResourceLimit` — using a `#[cfg(test)]`
  blocking host-fn fixture (a test-only import whose body `thread::sleep`s past
  `WALL_CLOCK_DEADLINE`), assert the call returns `ExecutionStatus::ResourceLimitExceeded` and that
  control returns to the caller within a test timeout comfortably above the deadline but far below
  the fixture's sleep (proving the kernel is unblocked *by the watchdog*, not by the sleep ending).
- New test `WallClock_NormalSkill_CompletesWithinDeadline_NotKilled` — the real `read-file` skill runs
  to `Ok` well under the deadline and is unaffected.
- New test `WallClock_TerminationSurfacedDistinctFromDeniedAndOk` — the timeout maps to
  `ResourceLimitExceeded`, never `Denied`/`Ok` (keeps it distinct from a policy denial, mirroring the
  fuel path's own distinctness test).
- Existing fuel/memory/table SR-6 tests still green (the watchdog is additive, not a replacement).
- All three merge gates green.

**Owner:** backend-engineer (security-architect deep-audit gate — p0-security; this is the p0
cross-cutting mechanism, review the detach/leak residual and the deadline value explicitly).
**Depends-on:** H2 (merge-order only — both edit `execute.rs`; H3 rebases on H2's merged sanitizer to
avoid an `execute.rs` conflict). No logical dependency on H1/H2. **Solo in Wave 2** (execute.rs
overlap with H2 precludes same-wave parallelism).

---

### H4 — #38 item 1: structured `tool_calls`/`tool_call_id` on `pythia_provider::Message` + contract suite

**Parent:** #38. **Partially-closes:** #38 (the frozen-seam half everything else builds on).
**Wave:** 3. **p0-security: no.** **Crate:** `pythia-provider`.

**Goal:** Extend the wire-agnostic `Message` so an assistant turn can carry structured `tool_calls`
and a `Role::Tool` turn can carry a `tool_call_id`, replacing the text-rendered `[tool_call …]`
convention — the precondition for any strict OpenAI-dialect provider. This is the frozen seam
(`Message`'s own doc comment flags it), so it lands first and TDD-first.

**Files:** `crates/provider/src/lib.rs` (`Message`, its constructors), `crates/provider/src/mock.rs`
(if the double constructs `Message`s), `crates/provider/src/contract_tests.rs` (extend the reusable
suite), `crates/provider/tests/*` (contract-against-mock).

**Approach:** Add optional structured correlation to `Message` without breaking the existing
`role`+`content` shape: e.g. `tool_calls: Vec<ToolCall>` (default empty, `skip_serializing_if`) on an
assistant message and `tool_call_id: Option<String>` on a tool message. Preserve the derived
`Serialize`/`Deserialize` round-trip contract the event log's `payload_json` relies on (extend the
existing `wire_type_serde_tests`). Extend the shared `run_provider_contract_tests` suite with a
tool-call-correlation case (assistant emits a `tool_calls[]` with an id → tool message carries the
matching `tool_call_id`) and keep the empty-messages/text-only/multi-chunk cases intact. Run the
extended suite against `MockProvider` to prove the harness before H5 consumes it.

**Done when:**
- New/extended contract test `Contract_AssistantToolCallThenToolResult_CorrelatesById` passes against
  `MockProvider`.
- New serde test asserting the new fields round-trip and that an assistant message with no tool calls
  and a tool message with no id serialize to the *same* shape as before (no wire regression for the
  existing types).
- All existing `provider` tests green.
- All three merge gates green.

**Owner:** backend-engineer.
**Depends-on:** none (Lane A seam; Wave 3 entry). Parallel-safe with H7, H8, H9, H11 (disjoint
crates/files).

---

### H5 — #38 item 2: Ollama wire translation for structured tool-calls

**Parent:** #38. **Partially-closes:** #38 (wire half).
**Wave:** 4. **p0-security: no.** **Crate:** `pythia-provider-ollama`.

**Goal:** Translate the new structured `Message` fields to/from the OpenAI-compatible wire so
`OllamaProvider` speaks `assistant.tool_calls[]` / `tool.tool_call_id` correctly, keeping every
Ollama-specific quirk contained in `wire.rs` (ADR-0005).

**Files:** `crates/provider-ollama/src/wire.rs` (`WireMessage` and its `From<&Message>`), possibly
`src/lib.rs` if request assembly changes.

**Approach:** `WireMessage` currently carries only `role`+`content`. Extend outbound translation so an
assistant `Message` with `tool_calls` serializes them into the wire `tool_calls[]` shape and a tool
`Message` with `tool_call_id` serializes it onto the wire tool turn. Reuse the existing
`deserialize_arguments` normalization for inbound arguments. Keep the existing text-only and
stringified-arguments handling untouched.

**Done when:**
- New test `Wire_AssistantMessageWithToolCalls_SerializesToOpenAiToolCallsShape`.
- New test `Wire_ToolMessageWithToolCallId_SerializesToolCallIdOnWire`.
- Extended mocked-HTTP contract run (from H4's suite) against `OllamaProvider` green; every
  `Live_Ollama_*` test stays `#[ignore]`.
- Existing `wire.rs` tests green.
- All three merge gates green.

**Owner:** backend-engineer.
**Depends-on:** H4 (consumes the extended `Message`). Parallel-safe with H6, H10 (disjoint crates:
provider-ollama vs kernel vs cli).

---

### H6 — #38 item 3: switch kernel `build_context` to structured tool-call fields

**Parent:** #38. **Closes:** #38 (final consumer; closes the tracking issue once H4+H5+H6 merge).
**Wave:** 4. **p0-security: no.** **Crate:** `pythia-kernel` (`src/context.rs`).

**Goal:** Replace `build_context`'s text-rendered `"{text}\n[tool_call name=… arguments=…]"` /
`"tool=… status=… output=…"` messages with the structured `tool_calls`/`tool_call_id` fields from H4,
so the context the kernel replays is strict-provider-ready.

**Files:** `crates/kernel/src/context.rs` (`event_to_message`, the `LlmResponse{tool_call}` and
`ToolResult` arms).

**Approach:** Emit an assistant `Message` carrying a structured `tool_calls[]` for an
`LlmResponse{tool_call: Some(_)}`, and a tool `Message` carrying the matching `tool_call_id` for the
following `ToolResult`. **Correlation without a schema change (frozen data model):** synthesize a
deterministic, replay-stable `tool_call_id` at context-build time from the event's position in
`history` (e.g. its index / the seq ordering already present) — do **not** add an id field to
`KernelEvent` or the event schema. Because `build_context` is rebuilt from history on every call, a
position-derived id correlates the assistant tool-call with its result within the same build and is
identical on replay. Cross-reference H8's SR-9 dedup-key derivation so both use the same
deterministic-from-history approach (consistency, not a hard dependency). Keep the "send the whole
turn, nothing dropped" KISS invariant (the `Compaction_SendsFullTurnHistory_NoEventsDropped`
regression guard must stay green).

**Done when:**
- New test `BuildContext_LlmResponseWithToolCall_EmitsStructuredToolCallsNotText` — the assistant
  message carries structured `tool_calls`, and no `[tool_call …]` text sentinel remains.
- New test `BuildContext_ToolResultFollowingToolCall_CarriesMatchingToolCallId` — the tool message's
  `tool_call_id` matches the preceding assistant tool-call's synthesized id, deterministically for a
  fixed history.
- Existing `Compaction_SendsFullTurnHistory_NoEventsDropped` and empty-history tests still green
  (update their assertions to the structured shape where they inspected `.content` text).
- All three merge gates green.

**Owner:** backend-engineer.
**Depends-on:** H4 (consumes the extended `Message`). Parallel-safe with H5, H10.

---

### H7 — #39 item 1: SR-8 denial reason inherits triggering `LlmResponse` taint

**Parent:** #39. **Closes:** #39 item 1 (SR-8 taint).
**Wave:** 3. **p0-security: no** (parent unlabeled) — **security-sensitive: security-architect audit
recommended.** **Crate:** `pythia-kernel` (`src/dispatch.rs`, `src/lib.rs`).

**Goal:** The unregistered-tool denial in `dispatch_tool` currently builds its `ToolResult` with
`tainted: false`, but `tool_call.name` came from a tainted `LlmResponse` (the LLM is always an
untrusted source — `llm_response_from_chunks` sets `tainted: true`). A denial whose very reason echoes
tainted, LLM-controlled bytes must inherit that taint (or carry no tainted content), so a downstream
taint pre-check (data-model §7) is not fed laundered-clean data.

**Files:** `crates/kernel/src/dispatch.rs` (`dispatch_tool` signature + the unregistered-tool arm),
`crates/kernel/src/lib.rs` (the one `dispatch_tool(&tool_call, …)` call site in `drive_turn`, which
already holds the triggering event).

**Approach:** Thread the triggering event's taint into `dispatch_tool` — add a `triggering_tainted:
bool` parameter set from the `LlmResponse` that produced the `tool_call` (available at the
`DispatchTool` call site in `drive_turn`; it is `true` for any real provider-issued call). Use it as
the `tainted` field of the unregistered-tool denial `ToolResult` (which embeds
`no skill registered for tool {name}` with the tainted name). Prefer inheriting the taint over
emptying the reason so the audit trail keeps the (now-correctly-tainted) diagnostic. Confirm the
granted/denied/resource-limit arms remain consistent (they already derive `tainted` from
`result.is_tainted()`, itself seeded from the skill's declared taint — leave those unless the audit
finds the same laundering there).

**Done when:**
- New test `Dispatch_UnregisteredTool_DenialInheritsTriggeringLlmResponseTaint` — a tool call whose
  triggering taint is `true` yields a denial `ToolResult` with `tainted == true`.
- Existing `Dispatch_UnregisteredTool_DeniedResultNotPanic` updated to pass the new parameter and
  still assert `status == "denied"`.
- Existing `Dispatch_ReadFileSkillResult_TaintedFlagSetTrue` still green.
- All three merge gates green.

**Owner:** backend-engineer (security-architect audit recommended).
**Depends-on:** none (Lane B; Wave 3 entry). Parallel-safe with H4, H8, H9, H11 (disjoint
crates/files; note H6 also edits kernel but `context.rs` ≠ `dispatch.rs` and lands in Wave 4).

---

### H8 — #39 item 2: SR-9 exactly-once — dedup-key design decision + manifest effect-class seam

**Parent:** #39. **Closes:** #39 item 2 (SR-9) *as a settled design + seam*; enforcement deferred to
the net-send milestone. **Wave:** 3. **p0-security: no** (parent unlabeled) — **security-sensitive:
security-architect audit recommended.** **Crate:** `pythia-manifest` + docs. **Settles Decision B.**

**Goal:** Settle SR-9 (per Decision B: deterministic dedup key, not two-phase journalling), record it
as a written contract, and land the manifest declaration seam a future non-idempotent effect will key
off — without editing the kernel dispatch path (which stays file-disjoint from H7 and carries no
enforcement until a non-idempotent effect exists).

**Files:** `docs/superpowers/security/pythia-exactly-once.md` (new design note stating the decision,
the key-derivation formula `f(turn_id, triggering seq, tool name, args-hash)`, the receiver-side
contract, and the human-confirm fallback for receivers that can't dedup),
`crates/manifest/src/manifest.rs` (add an `effect_class` / `idempotent` declaration to
`SkillManifest`, defaulting to the safe/idempotent-unknown value), and a `#[ignore]`d executable spec
test pinning the key's determinism.

**Approach:**
- Write the design note (mirrors this plan's Decision B; the durable source of truth the net-send
  milestone implements against). Explicitly state resume-time behavior: the derived key is a pure
  function of history, so replay re-derives it identically; a receiver that honors it dedups the
  retry; a receiver that cannot forces the manifest-declared non-idempotent effect down the
  human-confirm path.
- Add the manifest seam: a per-skill `effect_class` (e.g. `idempotent` | `non_idempotent`), TOML
  (de)serialized, defaulting fail-safe (an undeclared effect is treated as *not* auto-retryable).
  Round-trip it through `pythia-manifest`'s existing parser tests. Do **not** wire it into
  `dispatch.rs`/`lib.rs` yet (no enforcement this milestone).
- Land `DedupKey_DerivedFromHistory_IsDeterministicAcrossReplay` as a `#[cfg(test)]` (or `#[ignore]`d
  executable-spec) test over a pure `derive_dedup_key(...)` helper, proving two derivations from the
  same history bytes are byte-identical — the contract the net-send milestone will enforce.

**Done when:**
- `docs/superpowers/security/pythia-exactly-once.md` exists and states the decision + derivation +
  fallback.
- `SkillManifest`'s new `effect_class` field round-trips through TOML (new
  `Manifest_ParseEffectClass_RoundTrips` and a default-when-absent test).
- The determinism spec test passes (or is a green `#[cfg(test)]` unit test if the pure helper lands
  here; `#[ignore]`d if it depends on kernel wiring not built yet).
- All three merge gates green.

**Owner:** backend-engineer (security-architect audit recommended — this is the SR-9 contract).
**Depends-on:** none (Lane B; Wave 3 entry). Parallel-safe with H4, H7, H9, H11 (docs + manifest
crate, disjoint from kernel/provider/cli edits).

---

### H9 — #41 item 1: extract pure `Config::from_vars(lookup)` + real fallback coverage

**Parent:** #41. **Closes:** #41 item 1.
**Wave:** 3. **p0-security: no.** **Crate:** `pythia-cli` (`src/compose.rs`).

**Goal:** `Config::from_env` has no real coverage — the existing `Config_FromEnv_MissingVars_
FallsBackToDefaults` test constructs a `Config` by hand and asserts constants (a tautology; it never
exercises `from_env`'s lookup/fallback logic). Extract the pure lookup so the fallback path is
genuinely tested without touching process-global env state.

**Files:** `crates/cli/src/compose.rs` (`Config::from_env` → thin wrapper over a pure
`Config::from_vars(lookup: impl Fn(&str) -> Option<String>)`).

**Approach:** Introduce `Config::from_vars(lookup)` containing all the current fallback logic (db path
default, base-url default, optional model, optional policy path), keyed off the existing `ENV_*`
constants via the injected `lookup`. `from_env` becomes `Self::from_vars(|k| env::var(k).ok())`. Now
the fallback path is testable with a stub closure, deterministically and parallel-safe (no real env
mutation).

**Done when:**
- New test `FromVars_AllUnset_FallsBackToDefaults` — a lookup returning `None` for everything yields
  the documented defaults (`pythia.db`, `http://localhost:11434`, `None`, `None`).
- New test `FromVars_AllSet_ReadsEachVar` — a lookup returning distinct values for each `ENV_*` key
  produces a `Config` carrying each.
- New test `FromVars_PolicyPathSet_ParsedAsPathBuf`.
- The old tautological `Config_FromEnv_*` test is replaced (not merely duplicated).
- All three merge gates green.

**Owner:** backend-engineer.
**Depends-on:** none (Lane B; Wave 3 entry). Parallel-safe with H4, H7, H8, H11 (disjoint files; note
H10 also edits `compose.rs` and is sequenced after H9).

---

### H10 — #41 item 2: `build_kernel` skills parameter (live `pythia run` can register a skill)

**Parent:** #41. **Closes:** #41 item 2.
**Wave:** 4. **p0-security: no.** **Crate:** `pythia-cli` (`src/compose.rs`).

**Goal:** `build_kernel` hardcodes `skills: HashMap::new()`, so live `pythia run` can never dispatch a
real skill. Add a skills parameter so the composition root can register one (e.g. the compiled
`read-file` skill) into the `Kernel`.

**Files:** `crates/cli/src/compose.rs` (`build_kernel` signature + the `skills` binding), and its call
site(s) in `crates/cli/src/lib.rs`/`main.rs` (thread the skills map through — confirm whether the CLI
entry constructs its own map or the demos supply one; keep the composition root's own default a
documented explicit choice, not a silent empty map).

**Approach:** Change `build_kernel(config)` → `build_kernel(config, skills: HashMap<String,
SkillConfig>)` (or a `&[SkillConfig]` it collects), passing it straight into `Kernel::new` in place of
the hardcoded empty map. Update the entry point to build the map (for the slice, this can register the
compiled `read-file` module the same way the demo tests do). Preserve the fail-closed policy default
behavior unchanged.

**Done when:**
- New test `BuildKernel_WithSkills_KernelDispatchesRegisteredSkill` (or an in-process assertion that
  the constructed `Kernel` carries the provided skills) — a non-empty skills map reaches the
  `Kernel`.
- Existing `BuildKernel_ValidConfig_Succeeds` updated to the new signature and still green.
- All three merge gates green.

**Owner:** backend-engineer.
**Depends-on:** H9 (merge-order — both edit `compose.rs`; H10 rebases on H9's `from_vars`
refactor to avoid a `compose.rs` conflict). Parallel-safe with H5, H6 (disjoint crates).

---

### H11 — #41 item 3: strip control/ANSI chars when rendering tainted content (SR-17)

**Parent:** #41. **Closes:** #41 item 3.
**Wave:** 3. **p0-security: no** (parent unlabeled) — **security-sensitive: security-architect audit
recommended** (terminal-injection surface before a non-local channel; SR-17-adjacent). **Crate:**
`pythia-cli` (`src/render.rs`).

**Goal:** `render.rs` writes tainted bytes raw to stdout — an LLM- or file-content-derived
`ToolResult.output`/`LlmResponse.text` can carry ANSI escapes / non-printable control chars that
rewrite the operator's terminal (clear screen, spoof prompts, hidden cursor moves). Strip
non-printable/ANSI control characters when rendering **tainted** content, before it reaches stdout.

**Files:** `crates/cli/src/render.rs` (`render_event` — the `ToolResult` and `LlmResponse` arms,
which carry `tainted`).

**Approach:** Add a private `sanitize_for_terminal(&str) -> String` that strips/escapes C0 control
chars (except `\n`/`\t` which are legitimate output formatting), `0x7f`, and ANSI CSI/OSC sequences,
replacing them with a visible placeholder. Apply it to the operator-facing `text`/`output` fields of
events whose `tainted` flag is set (the LLM and tool-output surfaces); untainted, kernel-authored
content (e.g. `> {user text}` the operator just typed, terminal markers) renders verbatim. This
preserves the existing redaction-marker invariant (`<redacted:secret:…>` is printable and untouched —
keep `Render_ToolResultContainingRedactionMarker_PrintsMarkerVerbatim` green).

**Done when:**
- New test `Render_TaintedOutputWithAnsiEscapes_StrippedBeforeStdout` — a tainted `ToolResult.output`
  containing `\x1b[2J\x1b[H` and a `\0` renders with none of those raw control bytes and with the
  placeholder.
- New test `Render_UntaintedUserCommand_RenderedVerbatim` — control-char stripping does not touch
  untainted, operator-authored content (or, if the policy is to sanitize all output, assert the
  chosen rule explicitly — settle it in the task and name it, don't leave it implicit).
- Existing `Render_ToolResultContainingRedactionMarker_PrintsMarkerVerbatim` and
  `Render_ToolResultOk_PrintsOutputVerbatim` still green (their content is printable).
- All three merge gates green.

**Owner:** backend-engineer (security-architect audit recommended).
**Depends-on:** none (Lane B; Wave 3 entry). Parallel-safe with H4, H7, H8, H9 (disjoint files; note
H9/H10 edit `compose.rs`, this edits `render.rs` — file-disjoint).

---

## 3. Wave structure (for `project-manager` transcription)

Each wave = one epic; each task = one issue; dependencies mirror the per-task "Depends-on" notes.

- **Wave 1 — p0 capability-host input-hardening** *(parallel-safe: H1, H2)* — H1 (#36 read_memory
  bounds, `lib.rs`), H2 (#36 reason-string sanitize, `execute.rs`).
- **Wave 2 — p0 wall-clock watchdog** — H3 (#34, `execute.rs`+`limits.rs`; Decision A). Solo
  (`execute.rs` overlap with H2 → rebases on H2).
- **Wave 3 — Lane A seam + Lane B** *(parallel-safe: H4, H7, H8, H9, H11 — all disjoint files/crates)*
  — H4 (#38 `Message`+contract, provider), H7 (#39 SR-8 taint, kernel/`dispatch.rs`), H8 (#39 SR-9
  design+manifest seam; Decision B), H9 (#41 `from_vars`, cli/`compose.rs`), H11 (#41 render
  control-strip, cli/`render.rs`).
- **Wave 4 — Lane A consumers + CLI skills param** *(parallel-safe: H5, H6, H10 — disjoint crates)* —
  H5 (#38 ollama wire, provider-ollama), H6 (#38 `build_context` structured, kernel/`context.rs`),
  H10 (#41 `build_kernel` skills param, cli/`compose.rs`; rebases on H9).

**Critical path:** H2 → H3 (p0 complete), then H4 → {H5, H6} (Lane A). Lane B (H7, H8, H9→H10, H11)
runs concurrently with Lane A and is off the critical path.

**Genuinely parallel-safe lanes once Wave 3 opens:**
- Provider lane: H4 → {H5, H6}
- Kernel-taint lane: H7 (independent of everything)
- SR-9-design lane: H8 (docs + manifest, independent)
- CLI lane: {H9 → H10} and H11 (file-disjoint within cli)

---

## 4. Summary table

| Task | Title | Owner | Wave | Parallel-safe (within wave) | Parent issue | p0? |
|---|---|---|---|---|---|---|
| **H1** | `read_memory` bounds-check before alloc | backend-engineer | 1 | yes (w/ H2) | #36 (item 1) | **p0-security** |
| **H2** | Sanitize guest-controlled reason strings | backend-engineer | 1 | yes (w/ H1) | #36 (item 2) | **p0-security** |
| **H3** | SR-6 wall-clock watchdog (worker-thread) | backend-engineer | 2 | no (solo; rebases on H2) | #34 | **p0-security** |
| **H4** | Structured `tool_calls`/`tool_call_id` on `Message` + contract suite | backend-engineer | 3 | yes (w/ H7,H8,H9,H11) | #38 (item 1) | no |
| **H5** | Ollama wire translation for tool-calls | backend-engineer | 4 | yes (w/ H6,H10) | #38 (item 2) | no |
| **H6** | Kernel `build_context` structured fields | backend-engineer | 4 | yes (w/ H5,H10) | #38 (item 3) | no |
| **H7** | SR-8 denial reason inherits triggering taint | backend-engineer | 3 | yes (w/ H4,H8,H9,H11) | #39 (item 1) | no (security-sensitive) |
| **H8** | SR-9 dedup-key decision + manifest seam | backend-engineer | 3 | yes (w/ H4,H7,H9,H11) | #39 (item 2) | no (security-sensitive) |
| **H9** | Pure `Config::from_vars` + real coverage | backend-engineer | 3 | yes (w/ H4,H7,H8,H11) | #41 (item 1) | no |
| **H10** | `build_kernel` skills param | backend-engineer | 4 | yes (w/ H5,H6; rebases on H9) | #41 (item 2) | no |
| **H11** | Strip control/ANSI chars for tainted render | backend-engineer | 3 | yes (w/ H4,H7,H8,H9) | #41 (item 3) | no (security-sensitive) |

**p0-security deep-audit gate:** H1, H2, H3 (parents #34, #36). **Security-architect audit
recommended (not p0-gated):** H7, H8, H11.

**Cross-cutting decisions settled:** Decision A (#34) = **worker-thread + bounded join** (H3);
Decision B (#39 SR-9) = **deterministic dedup key**, SR-9 independent of #38 (H8).
