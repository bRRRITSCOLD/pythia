# Pythia First Vertical Slice — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the thinnest end-to-end Pythia slice: a Bubble Tea TUI drives an agent-core turn loop that reasons via Ollama (qwen3.5) behind a `Provider` port, calls 4 built-in tools (read/write/bash/edit) behind a `ToolRegistry`, and persists the session to SQLite behind a `SessionRepository` — one CGO-free `go build` binary.

**Architecture:** Ports-and-adapters with a strict inward dependency rule. `internal/core` owns the domain types, the four ports, and the synchronous turn loop, and imports only the Go standard library. `internal/adapter/*` implements the ports against real infrastructure. `cmd/pythia` is the sole composition root where everything is wired via DI. All decisions are already made in the architecture, data, and spec docs — this plan only sequences them.

**Tech Stack:** Go module `github.com/bRRRITSCOLD/pythia` · Bubble Tea / Bubbles / Lip Gloss (TUI) · `modernc.org/sqlite` (pure-Go, CGO-free) · Ollama `/api/chat` over HTTP · `go-playground/validator` (tool-arg validation) · `go test` (unit/integration) + `charmbracelet/x/exp/teatest` (e2e).

**Source-of-truth docs (bind exactly, do not re-decide):**
- Spec: `docs/superpowers/specs/first-slice.md`
- Architecture (ports, topology, dependency rule, NFRs, SR-1..SR-7, ADRs): `docs/architecture/first-slice.md`
- Data schema (SQLite DDL, migrator, PRAGMAs): `docs/data/first-slice-schema.md`
- Stack profile: `.ai/stack-profile.md`

## Global Constraints

Every task's requirements implicitly include this section.

- **Module path:** `github.com/bRRRITSCOLD/pythia`. Go 1.22+ (`go` directive `1.22`).
- **Dependency rule (load-bearing invariant):** dependencies point inward. `internal/core` imports **only the Go standard library** — no third-party libs, no `internal/adapter/*`. `internal/adapter/*` imports `internal/core` + third-party libs. `cmd/pythia` imports everything. **Any task that makes `internal/core` import an adapter package or a third-party runtime lib is WRONG and must be rejected in review.** Enforced by the fitness test in Task 1.
- **CGO-free:** `CGO_ENABLED=0 go build ./...` must succeed and produce one binary. Pure-Go deps only (`modernc.org/sqlite`, Bubble Tea). No `go-plugin`/gRPC, no chromem-go in this slice (YAGNI).
- **Port signatures are frozen** in `docs/architecture/first-slice.md` §2. Copy them verbatim; do not invent new ones or alter names/types.
- **ID generation stays stdlib:** message/session IDs are generated in `internal/core` with `crypto/rand`+`encoding/hex` (both stdlib) — never a third-party UUID lib (that would break the dependency rule).
- **Test naming (invariant):** `Subject_Scenario_Expectation`, per `principles-tdd`. Tiers are unit / integration / e2e. Integration and e2e tests fail loud (no silent skips) except the one build-tagged live-Ollama test (Task 8).
- **Security requirements (must land where cited):** SR-1 terminal-escape sanitization at the TUI render boundary · SR-2 workspace path containment in read/write/edit · SR-3 bash subprocess timeout+workdir+no-extra-secrets · SR-4a loop bound · SR-4b read byte cap · SR-4c bash output+time cap · SR-5 tool-arg validation at adapter boundary · SR-6 parameterized SQL only · SR-7 no content logging at info level.
- **TDD:** red → green → refactor per task. Commit at the end of each task (small, revertible, one PR per task).

---

## Wave / Dependency Table

Each task is one PR. `blockedBy` lists the tasks that must merge first. Tasks in the same wave with disjoint files are **parallel-safe** (build them concurrently to maximize the orchestrate loop).

| # | Task | Wave | blockedBy | Files (package) | Parallel-safe with |
|---|------|------|-----------|-----------------|--------------------|
| T1 | Module init + fitness tests (dep-direction, CGO-free) | 0 | — | `go.mod`, `internal/arch/*` | — (first) |
| T2 | Core domain types + sentinel errors | 0 | T1 | `internal/core/domain.go`, `errors.go`, `id.go` | T5 |
| T3 | Core ports (Provider, Tool, ToolRegistry, SessionRepository) | 0 | T2 | `internal/core/provider.go`, `tool.go`, `session.go` | T4 |
| T4 | Core AgentEvent contract | 0 | T2 | `internal/core/event.go` | T3, T5 |
| T5 | Config (env → validated Config) | 0 | T1 | `internal/config/config.go` | T2, T3, T4 |
| T6 | Core Agent turn loop | 1 | T3, T4 | `internal/core/agent.go` | T7, T8, T9, T10 |
| T7 | SQLite SessionRepository adapter + migrations | 1 | T3 | `internal/adapter/store/sqlite/*` | T6, T8, T9, T10 |
| T8 | Ollama Provider adapter (streaming) | 1 | T3 | `internal/adapter/provider/ollama/*` | T6, T7, T9, T10 |
| T9 | Tool toolkit (arg-validation + path containment + result envelope) | 1 | T2 | `internal/adapter/tool/toolkit/*` | T6, T7, T8, T10 |
| T10 | In-process ToolRegistry adapter | 1 | T3 | `internal/adapter/tool/registry/*` | T6, T7, T8, T9 |
| T11 | `read` tool | 2 | T9 | `internal/adapter/tool/read/*` | T12, T13, T14, T15 |
| T12 | `write` tool | 2 | T9 | `internal/adapter/tool/write/*` | T11, T13, T14, T15 |
| T13 | `edit` tool | 2 | T9 | `internal/adapter/tool/edit/*` | T11, T12, T14, T15 |
| T14 | `bash` tool | 2 | T9 | `internal/adapter/tool/bash/*` | T11, T12, T13, T15 |
| T15 | TUI adapter (Bubble Tea) + SR-1 sanitizer | 2 | T4, T6 | `internal/adapter/tui/*` | T11, T12, T13, T14 |
| T16 | `cmd/pythia` composition root (DI wiring) | 3 | T5, T6, T7, T8, T10, T11, T12, T13, T14, T15 | `cmd/pythia/main.go` | T17 |
| T17 | e2e TUI journey (teatest) | 3 | T6, T15 | `internal/adapter/tui/e2e_test.go` | T16 |

**File-contention notes:** the four tools (T11–T14) each own their own package and are fully file-disjoint — genuine parallelism. `registry` (T10) does **not** import the tools (it holds `core.Tool` values passed in by `cmd`), so it never contends with the tool tasks. `cmd/pythia` (T16) touches only `cmd/pythia/main.go`; because every other package is a library, T16 conflicts with nothing until the end — it is deliberately the last task. T17 (e2e) lives in the `tui` package's `_test.go` and can be built alongside T16.

**Cross-cutting decisions locked by this plan (see the tasks for detail):**
1. **AgentEvent variants** (frozen, T4): `EventTextDelta`, `EventToolCallStarted`, `EventToolCallFinished`, `EventTurnComplete`, `EventError`.
2. **Tool arg-validation + path containment live in one shared package** `internal/adapter/tool/toolkit` (T9). Every tool depends on it; nothing else does. This is where SR-2 and SR-5 are implemented once.
3. **Tool result convention:** a tool returns `(json.RawMessage, error)`. A *soft* failure the model should see and react to (bad path, non-zero exit, truncation) is returned as a JSON envelope `{"error": "..."}` / `{"ok": ...}` with **nil** Go error, so the loop feeds it back to the model. A Go error is reserved for infrastructure failure that should surface as `EventError`. Envelope helpers `toolkit.Err(...)` / `toolkit.OK(...)`.
4. **SR-1 terminal-escape sanitization lives at the TUI render boundary** (`internal/adapter/tui`, T15) — applied to every `AgentEvent.TextDelta` and tool-result string before render. Never in core, never in the tools.
5. **Sentinel errors** (T2): `core.ErrSessionNotFound`, `core.ErrMaxIterations`. Tools never return these — they use the envelope.
6. **ID generation** (T2): `core.NewID()` via `crypto/rand` (stdlib) — keeps core dependency-clean.
7. **Config carries** (T5): `OllamaBaseURL`, `OllamaModel`, `WorkspaceRoot`, `DBPath`, `BashTimeout`, `MaxReadBytes`, `MaxBashOutputBytes`, `MaxIterations`, `SessionID`. Env-only, validated at startup.

---

## Task 1: Module init + architecture fitness tests

**Wave:** 0 · **blockedBy:** — · **PR:** small.

Locks the module path and the two continuously-verified fitness functions (arch doc §5): the dependency-direction rule and the CGO-free build. Written first so every later wave is guarded.

**Files:**
- Create: `go.mod` (module `github.com/bRRRITSCOLD/pythia`, `go 1.22`)
- Create: `internal/arch/doc.go` (package doc only — gives the test package a home)
- Test: `internal/arch/dependency_rule_test.go`
- Create: `Makefile` (targets: `build`, `test`, `check-cgo`)

**Interfaces:**
- Consumes: nothing.
- Produces: the module path all other tasks import under; a `make check-cgo` target later tasks and CI reuse.

- [ ] **Step 1: Init the module**

```bash
cd /home/blaine-richardson/Code/github/bRRRITSCOLD/pythia
go mod init github.com/bRRRITSCOLD/pythia
```

- [ ] **Step 2: Write the failing dependency-direction test**

`internal/arch/dependency_rule_test.go`:

```go
package arch_test

import (
	"go/build"
	"strings"
	"testing"
)

// Core_Package_ImportsOnlyStdlib asserts the load-bearing dependency rule:
// internal/core imports nothing outward (no internal/adapter, no third-party).
func Core_Package_ImportsOnlyStdlib(t *testing.T) {
	const module = "github.com/bRRRITSCOLD/pythia"
	pkg, err := build.Import(module+"/internal/core", "", 0)
	if err != nil {
		// Until core exists this errors; that is the RED state.
		t.Fatalf("import core: %v", err)
	}
	for _, imp := range pkg.Imports {
		if strings.HasPrefix(imp, module+"/internal/adapter") {
			t.Errorf("core imports adapter %q — dependency rule violated", imp)
		}
		if strings.Contains(imp, ".") { // dotted path ⇒ external module (has a domain)
			t.Errorf("core imports third-party %q — core must be stdlib-only", imp)
		}
	}
}

func TestCore_Package_ImportsOnlyStdlib(t *testing.T) { Core_Package_ImportsOnlyStdlib(t) }
```

> Rename the exported helper to the `TestXxx` Go convention via the wrapper shown; keep the `Subject_Scenario_Expectation` name as the intent-bearing inner function. (Apply this wrapper pattern wherever a designed case name is not a legal `TestXxx`.)

- [ ] **Step 3: Run — expect RED** (`go test ./internal/arch/...` fails: core does not import cleanly yet / package missing). Create `internal/arch/doc.go` with `package arch` and a one-line doc comment so the test package resolves; the test still fails until `internal/core` exists in T2. That is expected — this test goes GREEN when T2 lands and stays green as a guard.

- [ ] **Step 4: Add Makefile targets**

```makefile
build: ; CGO_ENABLED=0 go build ./...
test: ; go test ./...
check-cgo: ; CGO_ENABLED=0 go build ./... && echo "CGO-free build OK"
```

- [ ] **Step 5: Commit**

```bash
git add go.mod internal/arch Makefile
git commit -m "chore: init module + architecture fitness tests"
```

**Acceptance criteria:**
- `go mod init` done; module path is exactly `github.com/bRRRITSCOLD/pythia`.
- `make check-cgo` succeeds on an empty build.
- The dependency-direction test exists and will fail loudly if any future change makes `internal/core` import an adapter or a third-party lib.

**Test list:**
- `Core_Package_ImportsOnlyStdlib` (unit, fitness) — core imports only stdlib.
- (Deferred assertion made real by T2; the test is the permanent guard.)

---

## Task 2: Core domain types + sentinel errors + ID generator

**Wave:** 0 · **blockedBy:** T1 · **PR:** small.

The domain vocabulary every adapter and the turn loop bind to. Copy the types **verbatim** from arch doc §2.1. Add sentinel errors (§2.5) and a stdlib ID generator (keeps core dependency-clean).

**Files:**
- Create: `internal/core/domain.go`
- Create: `internal/core/errors.go`
- Create: `internal/core/id.go`
- Test: `internal/core/domain_test.go`, `internal/core/id_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces (frozen — all later tasks reference these exact names):
  - `Role` (`RoleSystem|RoleUser|RoleAssistant|RoleTool`)
  - `ToolCall{ID string; Name string; Args json.RawMessage}`
  - `Message{ID, SessionID string; Role Role; Content string; ToolCalls []ToolCall; ToolCallID string; CreatedAt time.Time}`
  - `Session{ID, Title string; CreatedAt, UpdatedAt time.Time}`
  - `ToolSchema{Name, Description string; Parameters json.RawMessage}`
  - `var ErrSessionNotFound = errors.New("session not found")`
  - `var ErrMaxIterations = errors.New("max tool-call iterations exceeded")`
  - `func NewID() string` — 16 random bytes hex-encoded via `crypto/rand`.

- [ ] **Step 1: Write `domain.go`** — copy arch doc §2.1 verbatim (Role, ToolCall, Message, Session, ToolSchema; imports `encoding/json`, `time`).

- [ ] **Step 2: Write `errors.go`**

```go
package core

import "errors"

var (
	ErrSessionNotFound = errors.New("session not found")
	ErrMaxIterations   = errors.New("max tool-call iterations exceeded")
)
```

- [ ] **Step 3: Write the failing ID test**

```go
package core

import "testing"

func TestNewID_TwoCalls_ProducesDistinctNonEmptyIDs(t *testing.T) {
	a, b := NewID(), NewID()
	if a == "" || b == "" {
		t.Fatal("NewID returned empty string")
	}
	if a == b {
		t.Fatalf("NewID collided: %q == %q", a, b)
	}
	if len(a) != 32 { // 16 bytes hex
		t.Fatalf("want 32 hex chars, got %d (%q)", len(a), a)
	}
}
```

- [ ] **Step 4: Implement `id.go`**

```go
package core

import (
	"crypto/rand"
	"encoding/hex"
)

// NewID returns a 128-bit random hex id. Uses only stdlib so core stays
// dependency-clean (no third-party UUID lib — that would break the rule).
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns a short read
	return hex.EncodeToString(b[:])
}
```

- [ ] **Step 5: Add a JSON round-trip test for `Message`**

```go
func TestMessage_JSONRoundTrip_PreservesToolCalls(t *testing.T) {
	m := Message{ID: "m1", SessionID: "s1", Role: RoleAssistant,
		ToolCalls: []ToolCall{{ID: "c1", Name: "read", Args: json.RawMessage(`{"path":"go.mod"}`)}}}
	b, err := json.Marshal(m)
	if err != nil { t.Fatal(err) }
	var got Message
	if err := json.Unmarshal(b, &got); err != nil { t.Fatal(err) }
	if got.ToolCalls[0].Name != "read" || string(got.ToolCalls[0].Args) != `{"path":"go.mod"}` {
		t.Fatalf("round-trip lost tool call: %+v", got)
	}
}
```

- [ ] **Step 6: Run tests + the T1 fitness test — expect GREEN**

Run: `go test ./internal/core/... ./internal/arch/...`
Expected: PASS (the dependency-direction test now resolves `internal/core` and confirms it imports only stdlib).

- [ ] **Step 7: Commit**

```bash
git add internal/core
git commit -m "feat(core): domain types, sentinel errors, id generator"
```

**Acceptance criteria:**
- Types match arch doc §2.1 field-for-field.
- `internal/core` imports only `encoding/json`, `time`, `errors`, `crypto/rand`, `encoding/hex` — stdlib only; T1 fitness test green.

**Test list:**
- `TestNewID_TwoCalls_ProducesDistinctNonEmptyIDs` (unit).
- `TestMessage_JSONRoundTrip_PreservesToolCalls` (unit).
- `Core_Package_ImportsOnlyStdlib` now GREEN (fitness).

---

## Task 3: Core ports — Provider, Tool, ToolRegistry, SessionRepository

**Wave:** 0 · **blockedBy:** T2 · **PR:** small.

The four seams every adapter implements and the turn loop consumes. Copy **verbatim** from arch doc §2.2–§2.4. These are interfaces + request/event structs only — no logic, so the "test" is a compile-time contract test with a trivial fake per port proving the signatures are satisfiable.

**Files:**
- Create: `internal/core/provider.go` (`ChatRequest`, `StreamEvent`, `Provider`)
- Create: `internal/core/tool.go` (`Tool`, `ToolRegistry`)
- Create: `internal/core/session.go` (`SessionRepository`)
- Test: `internal/core/ports_test.go`

**Interfaces:**
- Consumes: `Message`, `ToolSchema`, `ToolCall`, `Session` (T2).
- Produces (frozen):
  - `ChatRequest{Messages []Message; Tools []ToolSchema}`
  - `StreamEvent{TextDelta string; ToolCalls []ToolCall; Done bool; Err error}`
  - `Provider interface { Chat(ctx, ChatRequest) (<-chan StreamEvent, error) }`
  - `Tool interface { Schema() ToolSchema; Invoke(ctx, json.RawMessage) (json.RawMessage, error) }`
  - `ToolRegistry interface { Schemas() []ToolSchema; Get(name string) (Tool, bool) }`
  - `SessionRepository interface { CreateSession(ctx, Session) error; GetSession(ctx, id) (Session, error); AppendMessage(ctx, Message) error; Messages(ctx, sessionID) ([]Message, error) }`

- [ ] **Step 1: Write the three port files** — copy arch doc §2.2, §2.3, §2.4 verbatim (including the contract doc comments; they are the behavioral spec every adapter's tests assert).

- [ ] **Step 2: Write the compile-time contract test**

```go
package core

import (
	"context"
	"encoding/json"
	"testing"
)

// staticFakes proves each port is satisfiable and pins the exact signatures.
type fakeProvider struct{}
func (fakeProvider) Chat(context.Context, ChatRequest) (<-chan StreamEvent, error) { return nil, nil }

type fakeTool struct{}
func (fakeTool) Schema() ToolSchema { return ToolSchema{} }
func (fakeTool) Invoke(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil }

type fakeReg struct{}
func (fakeReg) Schemas() []ToolSchema         { return nil }
func (fakeReg) Get(string) (Tool, bool)       { return nil, false }

type fakeRepo struct{}
func (fakeRepo) CreateSession(context.Context, Session) error       { return nil }
func (fakeRepo) GetSession(context.Context, string) (Session, error){ return Session{}, nil }
func (fakeRepo) AppendMessage(context.Context, Message) error       { return nil }
func (fakeRepo) Messages(context.Context, string) ([]Message, error){ return nil, nil }

func TestPorts_Signatures_AreImplementable(t *testing.T) {
	var _ Provider = fakeProvider{}
	var _ Tool = fakeTool{}
	var _ ToolRegistry = fakeReg{}
	var _ SessionRepository = fakeRepo{}
}
```

- [ ] **Step 3: Run — expect GREEN** (`go test ./internal/core/...`). If it does not compile, a signature deviated from the arch doc — fix the port, not the test.

- [ ] **Step 4: Commit**

```bash
git add internal/core
git commit -m "feat(core): Provider, Tool, ToolRegistry, SessionRepository ports"
```

**Acceptance criteria:**
- All four ports compile with signatures identical to arch doc §2.2–§2.4.
- Fakes satisfy them (contract test green); T1 fitness still green (still stdlib-only).

**Test list:**
- `TestPorts_Signatures_AreImplementable` (unit, contract).

---

## Task 4: Core AgentEvent contract

**Wave:** 0 · **blockedBy:** T2 · **PR:** small. **Parallel-safe with T3, T5.**

The UI-facing event the TUI renders — locked here so the TUI (T15) binds to a stable target without importing `Provider`. Copy arch doc §2.5's event types verbatim.

**Files:**
- Create: `internal/core/event.go`
- Test: `internal/core/event_test.go`

**Interfaces:**
- Consumes: `ToolCall` (T2), `encoding/json`.
- Produces (frozen):
  - `AgentEventType` with `EventTextDelta, EventToolCallStarted, EventToolCallFinished, EventTurnComplete, EventError` (iota order fixed).
  - `AgentEvent{Type AgentEventType; TextDelta string; ToolCall *ToolCall; ToolResult json.RawMessage; Err error}`

- [ ] **Step 1: Write `event.go`** — copy arch doc §2.5 (the `AgentEventType` consts and `AgentEvent` struct). No `Agent` struct here — that is T6.

- [ ] **Step 2: Write a stringer + its test** (so the TUI and logs can name events; keeps events debuggable)

```go
func TestAgentEventType_String_CoversAllVariants(t *testing.T) {
	cases := map[AgentEventType]string{
		EventTextDelta: "TextDelta", EventToolCallStarted: "ToolCallStarted",
		EventToolCallFinished: "ToolCallFinished", EventTurnComplete: "TurnComplete",
		EventError: "Error",
	}
	for et, want := range cases {
		if got := et.String(); got != want {
			t.Errorf("%d: got %q want %q", et, got, want)
		}
	}
}
```

Implement `func (t AgentEventType) String() string` with a lookup slice.

- [ ] **Step 3: Run — expect GREEN.** Commit.

```bash
git add internal/core/event.go internal/core/event_test.go
git commit -m "feat(core): AgentEvent UI-facing contract"
```

**Acceptance criteria:**
- `AgentEvent` and the five event-type constants match arch doc §2.5, in the frozen iota order.
- Stringer covers all five variants.

**Test list:**
- `TestAgentEventType_String_CoversAllVariants` (unit).

---

## Task 5: Config — env → validated Config

**Wave:** 0 · **blockedBy:** T1 · **PR:** small. **Parallel-safe with T2, T3, T4.**

Environment-only config (spec decision 7), parsed and validated once at startup. Lives in `internal/config` (its own package — imported by `cmd` and passed as plain values into adapter constructors; adapters do **not** import config).

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `os`, `strconv`, `time`, `github.com/go-playground/validator/v10`.
- Produces:
  ```go
  type Config struct {
      OllamaBaseURL      string        `validate:"required,url"`
      OllamaModel        string        `validate:"required"`
      WorkspaceRoot      string        `validate:"required,dir"`
      DBPath             string        `validate:"required"`
      BashTimeout        time.Duration `validate:"required,gt=0"`
      MaxReadBytes       int64         `validate:"required,gt=0"`
      MaxBashOutputBytes int64         `validate:"required,gt=0"`
      MaxIterations      int           `validate:"required,gt=0"`
      SessionID          string        // optional; empty ⇒ cmd creates a new session
  }
  func Load() (Config, error) // reads env, applies defaults, validates
  ```
  Env vars + defaults: `PYTHIA_OLLAMA_BASE_URL`=`http://localhost:11434` · `PYTHIA_OLLAMA_MODEL`=`qwen3.5` · `PYTHIA_WORKSPACE_ROOT`=cwd · `PYTHIA_DB_PATH`=`./pythia.db` · `PYTHIA_BASH_TIMEOUT`=`30s` · `PYTHIA_MAX_READ_BYTES`=`1048576` · `PYTHIA_MAX_BASH_OUTPUT_BYTES`=`1048576` · `PYTHIA_MAX_ITERATIONS`=`10` · `PYTHIA_SESSION_ID`=`""`.

- [ ] **Step 1: `go get github.com/go-playground/validator/v10`.**

- [ ] **Step 2: Write the failing default-load test**

```go
func TestLoad_NoEnvSet_AppliesValidDefaults(t *testing.T) {
	t.Setenv("PYTHIA_WORKSPACE_ROOT", t.TempDir()) // dir validator needs a real dir
	cfg, err := Load()
	if err != nil { t.Fatalf("Load: %v", err) }
	if cfg.OllamaModel != "qwen3.5" { t.Errorf("model=%q", cfg.OllamaModel) }
	if cfg.BashTimeout != 30*time.Second { t.Errorf("timeout=%v", cfg.BashTimeout) }
	if cfg.MaxIterations != 10 { t.Errorf("maxIter=%d", cfg.MaxIterations) }
}
```

- [ ] **Step 3: Write failing negative tests**

```go
func TestLoad_InvalidBashTimeout_ReturnsError(t *testing.T) {
	t.Setenv("PYTHIA_WORKSPACE_ROOT", t.TempDir())
	t.Setenv("PYTHIA_BASH_TIMEOUT", "not-a-duration")
	if _, err := Load(); err == nil { t.Fatal("want error for bad duration") }
}
func TestLoad_NonexistentWorkspaceRoot_FailsValidation(t *testing.T) {
	t.Setenv("PYTHIA_WORKSPACE_ROOT", "/no/such/dir/pythia-xyz")
	if _, err := Load(); err == nil { t.Fatal("want validation error for missing dir") }
}
```

- [ ] **Step 4: Implement `Load()`** — read each env with a `getenv(key, default)` helper, `time.ParseDuration` / `strconv.ParseInt` the numeric ones (return a wrapped error on parse failure), default `WorkspaceRoot` to `os.Getwd()`, then `validator.New().Struct(&cfg)` and return.

- [ ] **Step 5: Run — expect GREEN.** Commit.

```bash
git add internal/config go.mod go.sum
git commit -m "feat(config): env-parsed validated Config"
```

**Acceptance criteria:**
- All env vars honored with the documented defaults.
- Invalid duration/int and a nonexistent workspace root both return an error (no panic).

**Test list:**
- `TestLoad_NoEnvSet_AppliesValidDefaults` (unit).
- `TestLoad_InvalidBashTimeout_ReturnsError` (unit, negative).
- `TestLoad_NonexistentWorkspaceRoot_FailsValidation` (unit, negative).
- `TestLoad_AllEnvSet_OverridesDefaults` (unit) — set every var, assert each field.

---

## Task 6: Core Agent turn loop

**Wave:** 1 · **blockedBy:** T3, T4 · **PR:** medium. **Parallel-safe with T7, T8, T9, T10** (binds to ports + fakes, not impls).

The synchronous turn loop (spec decision 1; arch doc §2.5, NFR loop-bound SR-4a). Drives `Provider`, `ToolRegistry`, `SessionRepository` — **all injected ports**. It emits the `AgentEvent` stream. **WRONG if it imports any `internal/adapter` package or a third-party lib** — it must compile against interfaces only; the T1 fitness test enforces this.

**Files:**
- Create: `internal/core/agent.go`
- Test: `internal/core/agent_test.go` (fakes for all three ports)

**Interfaces:**
- Consumes: `Provider`, `ToolRegistry`, `SessionRepository` (T3); `AgentEvent`/`AgentEventType` (T4); `Message`, `ToolCall`, `ErrMaxIterations`, `ErrSessionNotFound`, `NewID` (T2).
- Produces:
  - `type Agent struct{...}`
  - `func NewAgent(p Provider, reg ToolRegistry, repo SessionRepository, opts ...AgentOption) *Agent`
  - `type AgentOption func(*Agent)`
  - `func WithMaxIterations(n int) AgentOption`
  - `func (a *Agent) Send(ctx context.Context, sessionID, userInput string) (<-chan AgentEvent, error)`

**Loop contract (implement exactly):**
1. `Send` calls `repo.GetSession`; if it returns `ErrSessionNotFound`, `Send` returns that error synchronously (no channel).
2. Otherwise it launches a goroutine, returns the channel, and: persists the user `Message` (`NewID`, `RoleUser`, `time.Now().UTC()`).
3. Loop up to `maxIterations` (default 10): load `repo.Messages`, call `provider.Chat(ctx, ChatRequest{Messages, Tools: reg.Schemas()})`.
4. A setup error from `Chat` ⇒ emit `EventError` and stop. Drain the stream: each `TextDelta` ⇒ accumulate + emit `EventTextDelta`; a `StreamEvent.Err` ⇒ emit `EventError` and stop; at `Done`, capture `ToolCalls`.
5. Persist the assistant `Message` (accumulated text + tool calls).
6. If no tool calls ⇒ emit `EventTurnComplete` and stop.
7. Else for each tool call: emit `EventToolCallStarted`; `reg.Get(name)` — unknown ⇒ result envelope `{"error":"unknown tool <name>"}`; else `tool.Invoke(ctx, args)` — a Go error ⇒ emit `EventError` and stop (infra failure); a JSON result ⇒ persist a `RoleTool` `Message` (Content=result string, `ToolCallID`=call id) and emit `EventToolCallFinished`. Continue the loop.
8. On loop overflow: emit `AgentEvent{Type: EventError, Err: ErrMaxIterations}`.
9. Every channel send respects ctx: `select { case out <- ev: case <-ctx.Done(): return }`. Channel is always closed via `defer close(out)`.

- [ ] **Step 1: Write the happy-path failing test** (fakes: a scripted `Provider` that returns queued streams; an in-memory repo; a registry over a map of fake tools).

```go
func TestAgent_Send_NoToolCalls_EmitsDeltasThenTurnComplete(t *testing.T) {
	repo := newMemRepo(t) // implements SessionRepository, seeded with session "s1"
	prov := &scriptProvider{turns: [][]StreamEvent{{
		{TextDelta: "Hel"}, {TextDelta: "lo"}, {Done: true},
	}}}
	a := NewAgent(prov, emptyRegistry{}, repo)
	ch, err := a.Send(context.Background(), "s1", "hi")
	if err != nil { t.Fatal(err) }
	var text string
	var completed bool
	for ev := range ch {
		switch ev.Type {
		case EventTextDelta: text += ev.TextDelta
		case EventTurnComplete: completed = true
		case EventError: t.Fatalf("unexpected error: %v", ev.Err)
		}
	}
	if text != "Hello" || !completed { t.Fatalf("text=%q completed=%v", text, completed) }
	// user + assistant messages persisted
	msgs, _ := repo.Messages(context.Background(), "s1")
	if len(msgs) != 2 { t.Fatalf("want 2 persisted messages, got %d", len(msgs)) }
}
```

- [ ] **Step 2: Write the tool-round-trip test**

```go
func TestAgent_Send_OneToolCall_ExecutesThenReInvokesProviderToCompletion(t *testing.T) {
	repo := newMemRepo(t)
	prov := &scriptProvider{turns: [][]StreamEvent{
		{{Done: true, ToolCalls: []ToolCall{{ID: "c1", Name: "read", Args: json.RawMessage(`{"path":"go.mod"}`)}}}},
		{{TextDelta: "done"}, {Done: true}},
	}}
	reg := registryWith("read", toolFunc(func(ctx context.Context, a json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":"module x"}`), nil
	}))
	a := NewAgent(prov, reg, repo)
	ch, _ := a.Send(context.Background(), "s1", "what's in go.mod?")
	var started, finished, completed bool
	for ev := range ch {
		switch ev.Type {
		case EventToolCallStarted: started = ev.ToolCall.Name == "read"
		case EventToolCallFinished: finished = string(ev.ToolResult) == `{"ok":"module x"}`
		case EventTurnComplete: completed = true
		}
	}
	if !(started && finished && completed) { t.Fatalf("started=%v finished=%v completed=%v", started, finished, completed) }
	msgs, _ := repo.Messages(context.Background(), "s1")
	// user, assistant(tool-calls), tool-result, assistant(final) = 4
	if len(msgs) != 4 || msgs[2].Role != RoleTool || msgs[2].ToolCallID != "c1" {
		t.Fatalf("bad history: %+v", msgs)
	}
}
```

- [ ] **Step 3: Write the adversarial tests**

```go
func TestAgent_Send_UnknownSession_ReturnsErrSessionNotFound(t *testing.T) {
	a := NewAgent(&scriptProvider{}, emptyRegistry{}, newMemRepo(t))
	if _, err := a.Send(context.Background(), "nope", "hi"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("want ErrSessionNotFound, got %v", err)
	}
}

func TestAgent_Send_ProviderSetupError_EmitsEventErrorNoCrash(t *testing.T) {
	prov := &scriptProvider{setupErr: errors.New("ollama down")}
	a := NewAgent(prov, emptyRegistry{}, newMemRepo(t))
	ch, _ := a.Send(context.Background(), "s1", "hi")
	got := drainForError(ch)
	if got == nil { t.Fatal("want EventError for provider-down") }
}

func TestAgent_Send_MidStreamErr_EmitsEventError(t *testing.T) {
	prov := &scriptProvider{turns: [][]StreamEvent{{{TextDelta: "hi"}, {Err: errors.New("reset")}}}}
	a := NewAgent(prov, emptyRegistry{}, newMemRepo(t))
	if drainForError(a.Send(context.Background(), "s1", "hi")) == nil {
		t.Fatal("want EventError on mid-stream failure")
	}
}

func TestAgent_Send_ModelLoopsForever_StopsAtMaxIterationsWithErr(t *testing.T) {
	// every turn returns the same tool call ⇒ would loop forever
	loopTurn := []StreamEvent{{Done: true, ToolCalls: []ToolCall{{ID: "c", Name: "noop"}}}}
	turns := make([][]StreamEvent, 50)
	for i := range turns { turns[i] = loopTurn }
	prov := &scriptProvider{turns: turns}
	reg := registryWith("noop", toolFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	}))
	a := NewAgent(prov, reg, newMemRepo(t), WithMaxIterations(3))
	last := lastEvent(a.Send(context.Background(), "s1", "go"))
	if last.Type != EventError || !errors.Is(last.Err, ErrMaxIterations) {
		t.Fatalf("want ErrMaxIterations, got %+v", last)
	}
}

func TestAgent_Send_ToolInfraError_EmitsEventError(t *testing.T) {
	prov := &scriptProvider{turns: [][]StreamEvent{{{Done: true, ToolCalls: []ToolCall{{ID: "c", Name: "boom"}}}}}}
	reg := registryWith("boom", toolFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return nil, errors.New("disk exploded") // Go error ⇒ infra failure
	}))
	a := NewAgent(prov, reg, newMemRepo(t))
	if drainForError(a.Send(context.Background(), "s1", "go")) == nil {
		t.Fatal("want EventError on tool infra error")
	}
}

func TestAgent_Send_CtxCancelledMidStream_StopsAndClosesChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	prov := &scriptProvider{turns: [][]StreamEvent{{{TextDelta: "a"}, {TextDelta: "b"}, {Done: true}}}, onFirstDelta: cancel}
	a := NewAgent(prov, emptyRegistry{}, newMemRepo(t))
	ch, _ := a.Send(ctx, "s1", "hi")
	for range ch { } // must terminate (channel closes) despite cancel
}
```

- [ ] **Step 4: Run — expect RED** (Agent undefined). **Step 5: Implement `agent.go`** per the loop contract above (helper `emit(ev)` doing the ctx-aware select; `defer close(out)`). **Step 6: Run — expect GREEN**, including the T1 fitness test (still stdlib-only).

- [ ] **Step 7: Commit**

```bash
git add internal/core/agent.go internal/core/agent_test.go
git commit -m "feat(core): synchronous agent turn loop with MaxIterations guard"
```

**Acceptance criteria:**
- All nine loop-contract behaviors hold; `internal/core` still imports only stdlib (SR-4a loop bound enforced by `WithMaxIterations`, default 10).
- Provider-down and mid-stream errors surface as `EventError` without crashing; already-persisted messages remain valid (durability NFR).

**Test list (all unit, fakes for ports):**
- `TestAgent_Send_NoToolCalls_EmitsDeltasThenTurnComplete`
- `TestAgent_Send_OneToolCall_ExecutesThenReInvokesProviderToCompletion`
- `TestAgent_Send_UnknownSession_ReturnsErrSessionNotFound` (negative)
- `TestAgent_Send_ProviderSetupError_EmitsEventErrorNoCrash` (failure mode)
- `TestAgent_Send_MidStreamErr_EmitsEventError` (failure mode)
- `TestAgent_Send_ModelLoopsForever_StopsAtMaxIterationsWithErr` (SR-4a, boundary)
- `TestAgent_Send_ToolInfraError_EmitsEventError` (failure mode)
- `TestAgent_Send_UnknownTool_ReturnsErrorEnvelopeToModel` (negative — unknown tool becomes a `RoleTool` result the loop feeds back, not a crash)
- `TestAgent_Send_CtxCancelledMidStream_StopsAndClosesChannel` (concurrency/lifecycle)

---

## Task 7: SQLite SessionRepository adapter + migrations

**Wave:** 1 · **blockedBy:** T3 · **PR:** medium. **Parallel-safe with T6, T8, T9, T10.**

Implements `SessionRepository` behind SQLite exactly per `docs/data/first-slice-schema.md` (STRICT tables, JSON `tool_calls`, monotonic `seq`, WAL/FK/busy_timeout PRAGMAs, `user_version` migrator). **SR-6: parameterized queries only** — the sole literal interpolation permitted is the `PRAGMA user_version = <ordinal>` in the migrator (ordinal is adapter-controlled, never content). **SR-7:** do not log message content.

**Files:**
- Create: `internal/adapter/store/sqlite/sqlite.go` (constructor + PRAGMAs + `SetMaxOpenConns(1)`)
- Create: `internal/adapter/store/sqlite/repository.go` (the four port methods)
- Create: `internal/adapter/store/sqlite/migrate.go` (embed.FS + user_version migrator, data-doc §9 sketch)
- Create: `internal/adapter/store/sqlite/migrations/0001_init.sql` (data-doc §9 DDL verbatim)
- Test: `internal/adapter/store/sqlite/repository_test.go`, `migrate_test.go`

**Interfaces:**
- Consumes: `core.Session`, `core.Message`, `core.ToolCall`, `core.Role`, `core.ErrSessionNotFound` (T2/T3); `modernc.org/sqlite` (driver, registered as `"sqlite"`); `database/sql`, `embed`.
- Produces: `func New(path string) (*Repo, error)` where `*Repo` implements `core.SessionRepository`; `func (*Repo) Close() error`.

- [ ] **Step 1: `go get modernc.org/sqlite`.**

- [ ] **Step 2: Add `migrations/0001_init.sql`** — copy the DDL from data-doc §9 verbatim (both STRICT tables, CHECKs, FK, `UNIQUE(session_id, seq)`).

- [ ] **Step 3: Write the migrator failing test**

```go
func TestMigrate_FreshDB_CreatesSchemaAndSetsUserVersion(t *testing.T) {
	db := openTemp(t) // sql.Open("sqlite", tmpfile+dsn)
	if err := migrate(db); err != nil { t.Fatal(err) }
	var v int
	db.QueryRow("PRAGMA user_version").Scan(&v)
	if v != 1 { t.Fatalf("user_version=%d want 1", v) }
	// tables exist
	var n int
	db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('sessions','messages')").Scan(&n)
	if n != 2 { t.Fatalf("want 2 tables, got %d", n) }
}
func TestMigrate_AlreadyMigrated_IsNoOp(t *testing.T) {
	db := openTemp(t)
	migrate(db)
	if err := migrate(db); err != nil { t.Fatalf("second migrate: %v", err) }
}
```

- [ ] **Step 4: Implement `migrate.go`** — copy the data-doc §9 migrator sketch (embed.FS glob, sort, per-file tx, `user_version` gate + literal set).

- [ ] **Step 5: Write repository integration tests (real modernc engine, temp DB)**

```go
func TestRepo_AppendMessage_AssignsMonotonicSeqPerSession(t *testing.T) {
	r := newRepo(t) // New(tmpfile); creates session "s1"
	for i := 0; i < 3; i++ {
		_ = r.AppendMessage(ctx, core.Message{ID: core.NewID(), SessionID: "s1", Role: core.RoleUser, Content: "m", CreatedAt: time.Now().UTC()})
	}
	msgs, _ := r.Messages(ctx, "s1")
	if len(msgs) != 3 { t.Fatalf("want 3, got %d", len(msgs)) }
}
func TestRepo_Messages_ReturnsHistoryInSeqOrder(t *testing.T) { /* append u,a,tool; assert order + roles */ }
func TestRepo_AppendMessage_PersistsAndReloadsToolCallsJSON(t *testing.T) {
	r := newRepo(t)
	m := core.Message{ID: "m1", SessionID: "s1", Role: core.RoleAssistant,
		ToolCalls: []core.ToolCall{{ID: "c1", Name: "read", Args: json.RawMessage(`{"path":"go.mod"}`)}},
		CreatedAt: time.Now().UTC()}
	r.AppendMessage(ctx, m)
	got, _ := r.Messages(ctx, "s1")
	if got[0].ToolCalls[0].Name != "read" || string(got[0].ToolCalls[0].Args) != `{"path":"go.mod"}` {
		t.Fatalf("tool_calls JSON not round-tripped: %+v", got[0])
	}
}
func TestRepo_GetSession_Missing_ReturnsErrSessionNotFound(t *testing.T) {
	r := newRepo(t)
	if _, err := r.GetSession(ctx, "ghost"); !errors.Is(err, core.ErrSessionNotFound) {
		t.Fatalf("want ErrSessionNotFound, got %v", err)
	}
}
func TestRepo_AppendMessage_UnknownSession_ViolatesForeignKey(t *testing.T) {
	r := newRepo(t) // FK ON via PRAGMA
	err := r.AppendMessage(ctx, core.Message{ID: "x", SessionID: "no-session", Role: core.RoleUser, CreatedAt: time.Now().UTC()})
	if err == nil { t.Fatal("want FK violation for orphan message") }
}
func TestRepo_AppendMessage_BadRole_ViolatesCheckConstraint(t *testing.T) {
	// insert via a message with Role "hacker" ⇒ STRICT + CHECK rejects
}
func TestRepo_ResumeAcrossReopen_ReplaysHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	r1, _ := New(path); r1.CreateSession(ctx, core.Session{ID: "s1", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()})
	r1.AppendMessage(ctx, core.Message{ID: "m1", SessionID: "s1", Role: core.RoleUser, Content: "hi", CreatedAt: time.Now().UTC()})
	r1.Close()
	r2, _ := New(path); defer r2.Close()
	msgs, _ := r2.Messages(ctx, "s1")
	if len(msgs) != 1 || msgs[0].Content != "hi" { t.Fatalf("resume failed: %+v", msgs) }
}
```

- [ ] **Step 6: Implement `sqlite.go` + `repository.go`** — DSN with the three `_pragma` params (data-doc §8), `SetMaxOpenConns(1)`, run `migrate` in `New`. `AppendMessage` uses the correlated-subquery `seq` INSERT (data-doc §5) with `ID` defaulted to `core.NewID()` if empty; `tool_calls` marshalled to NULL when `len==0`; `tool_call_id` NULL when `""`. `GetSession` maps `sql.ErrNoRows` → `core.ErrSessionNotFound`. **All queries parameterized (SR-6).** `Messages` uses the §5 `ORDER BY seq` select. Timestamps `RFC3339Nano` UTC round-trip.

- [ ] **Step 7: Run — expect GREEN** (`go test ./internal/adapter/store/sqlite/...`). Confirm CGO-free: `CGO_ENABLED=0 go test ./internal/adapter/store/sqlite/...`. Commit.

```bash
git add internal/adapter/store/sqlite go.mod go.sum
git commit -m "feat(store/sqlite): SessionRepository adapter, migrations, PRAGMAs"
```

**Acceptance criteria:**
- Schema/PRAGMAs/migrator match the data doc; FK, role CHECK, and `UNIQUE(session_id, seq)` are enforced against a real engine.
- Resume across reopen replays history in `seq` order (durability NFR).
- Every query parameterized except the `user_version` literal (SR-6). No content logged (SR-7). CGO-free.

**Test list:**
- Migrator: `TestMigrate_FreshDB_CreatesSchemaAndSetsUserVersion`, `TestMigrate_AlreadyMigrated_IsNoOp` (integration).
- Repo happy: `TestRepo_AppendMessage_AssignsMonotonicSeqPerSession`, `TestRepo_Messages_ReturnsHistoryInSeqOrder`, `TestRepo_AppendMessage_PersistsAndReloadsToolCallsJSON`, `TestRepo_ResumeAcrossReopen_ReplaysHistory` (integration).
- Repo negative/constraints: `TestRepo_GetSession_Missing_ReturnsErrSessionNotFound`, `TestRepo_AppendMessage_UnknownSession_ViolatesForeignKey`, `TestRepo_AppendMessage_BadRole_ViolatesCheckConstraint` (integration).
- Contract reuse: run the shared `SessionRepository` contract cases (from T6's fakes' expectations) against this real adapter.

---

## Task 8: Ollama Provider adapter (streaming)

**Wave:** 1 · **blockedBy:** T3 · **PR:** medium. **Parallel-safe with T6, T7, T9, T10.**

Implements `core.Provider` against Ollama `/api/chat` with streaming + tool calling (spec decision 4, arch ADR-0001). Translates `ChatRequest` → Ollama request, and the NDJSON streaming response → `StreamEvent`s (TextDeltas, then one terminal `Done` optionally carrying `ToolCalls`; setup failure → returned error; mid-stream failure → `StreamEvent.Err`). Resolves the open question (native qwen3.5 tool-call wire format) against a faked Ollama HTTP server; a build-tagged live test hits a real Ollama.

**Files:**
- Create: `internal/adapter/provider/ollama/ollama.go` (`New(baseURL, model string, hc *http.Client) *Provider`, `Chat`)
- Create: `internal/adapter/provider/ollama/wire.go` (request/response structs mapping the Ollama dialect)
- Test: `internal/adapter/provider/ollama/ollama_test.go` (httptest faked stream)
- Test: `internal/adapter/provider/ollama/live_test.go` (`//go:build ollama_live` — opt-in)

**Interfaces:**
- Consumes: `core.ChatRequest`, `core.StreamEvent`, `core.Message`, `core.ToolCall`, `core.ToolSchema`, `core.Role` (T2/T3); `net/http`, `encoding/json`, `bufio`.
- Produces: `func New(baseURL, model string, hc *http.Client) *Provider` implementing `core.Provider`.

**Wire mapping (Ollama `/api/chat`, `"stream": true` — NDJSON, one JSON object per line):**
- Request: `{"model":..., "messages":[{"role","content","tool_calls"?, "tool_call_id"?}], "tools":[{"type":"function","function":{"name","description","parameters"}}], "stream":true}`. Map `core.Role` → Ollama role strings (identical values). `ToolSchema.Parameters` nests directly. `RoleTool` messages carry `tool_call_id`.
- Response lines: each has `message.content` (accumulate → `TextDelta`) and optionally `message.tool_calls`. The final line has `"done": true`. On the terminal line, translate `message.tool_calls` (each `{function:{name,arguments}}`) → `[]core.ToolCall` (assign an ID if Ollama omits one: `core.NewID()`), and emit a single `StreamEvent{Done: true, ToolCalls: ...}`.

- [ ] **Step 1: Write the streaming happy-path test against a faked server**

```go
func TestProvider_Chat_StreamsTextDeltasThenTerminalDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"Hel"},"done":false}`)
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"lo"},"done":false}`)
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":""},"done":true}`)
	}))
	defer srv.Close()
	p := New(srv.URL, "qwen3.5", srv.Client())
	ch, err := p.Chat(context.Background(), core.ChatRequest{Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}}})
	if err != nil { t.Fatal(err) }
	var text string; var sawDone bool
	for ev := range ch {
		if ev.Err != nil { t.Fatalf("err: %v", ev.Err) }
		text += ev.TextDelta
		if ev.Done { sawDone = true }
	}
	if text != "Hello" || !sawDone { t.Fatalf("text=%q done=%v", text, sawDone) }
}
```

- [ ] **Step 2: Write the tool-call translation test**

```go
func TestProvider_Chat_ToolCallsInResponse_DeliveredOnTerminalEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"read","arguments":{"path":"go.mod"}}}]},"done":true}`)
	}))
	defer srv.Close()
	p := New(srv.URL, "qwen3.5", srv.Client())
	ch, _ := p.Chat(context.Background(), core.ChatRequest{
		Messages: []core.Message{{Role: core.RoleUser, Content: "read go.mod"}},
		Tools:    []core.ToolSchema{{Name: "read", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	var tc []core.ToolCall
	for ev := range ch { if ev.Done { tc = ev.ToolCalls } }
	if len(tc) != 1 || tc[0].Name != "read" || string(tc[0].Args) != `{"path":"go.mod"}` {
		t.Fatalf("bad tool call: %+v", tc)
	}
}
```

- [ ] **Step 3: Write the failure-mode tests**

```go
func TestProvider_Chat_OllamaUnreachable_ReturnsSetupError(t *testing.T) {
	p := New("http://127.0.0.1:1/", "qwen3.5", &http.Client{Timeout: time.Second})
	if _, err := p.Chat(context.Background(), core.ChatRequest{}); err == nil {
		t.Fatal("want setup error when Ollama unreachable")
	}
}
func TestProvider_Chat_ConnectionDropsMidStream_EmitsStreamEventErr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"message":{"content":"partial"},"done":false}`)
		w.(http.Flusher).Flush()
		srvCloseConn(w) // hijack + close without a done line
	}))
	defer srv.Close()
	p := New(srv.URL, "qwen3.5", srv.Client())
	ch, _ := p.Chat(context.Background(), core.ChatRequest{})
	var sawErr bool
	for ev := range ch { if ev.Err != nil { sawErr = true } }
	if !sawErr { t.Fatal("want StreamEvent.Err on mid-stream drop") }
}
func TestProvider_Chat_CtxCancelled_AbortsAndClosesChannel(t *testing.T) { /* cancel mid-stream; channel closes */ }
```

- [ ] **Step 4: Implement `wire.go` + `ollama.go`** — POST JSON to `baseURL+"/api/chat"` with the request ctx; a non-2xx or dial error before the first line ⇒ return the setup error. Otherwise launch a goroutine scanning the body with `bufio.Scanner`, decode each line, emit `TextDelta` per non-empty content chunk, and on `done:true` emit the terminal `Done` event (with translated tool calls); a scanner/decode error mid-stream ⇒ emit `StreamEvent{Err: ...}`. `defer close(ch)`; honor ctx cancellation (request ctx aborts the read).

- [ ] **Step 5: Add the opt-in live test** (`//go:build ollama_live`): `TestProvider_Live_Chat_RealOllamaAnswers` hitting `PYTHIA_OLLAMA_BASE_URL`, skipped from the default `go test ./...` via the build tag. Documents the real wire format resolution.

- [ ] **Step 6: Run — expect GREEN** (default tests, faked server). Commit.

```bash
git add internal/adapter/provider/ollama go.mod go.sum
git commit -m "feat(provider/ollama): streaming Provider adapter over /api/chat"
```

**Acceptance criteria:**
- Streams `TextDelta`s then exactly one terminal `Done` (streaming responsiveness NFR — no batching).
- Tool calls delivered on the terminal event, translated to `core.ToolCall` (arg JSON preserved).
- Unreachable Ollama → setup error; mid-stream drop → `StreamEvent.Err`; ctx cancel closes the channel (graceful Ollama-down NFR). CGO-free.

**Test list:**
- `TestProvider_Chat_StreamsTextDeltasThenTerminalDone` (integration, faked HTTP).
- `TestProvider_Chat_ToolCallsInResponse_DeliveredOnTerminalEvent` (integration).
- `TestProvider_Chat_OllamaUnreachable_ReturnsSetupError` (failure mode).
- `TestProvider_Chat_ConnectionDropsMidStream_EmitsStreamEventErr` (failure mode).
- `TestProvider_Chat_CtxCancelled_AbortsAndClosesChannel` (concurrency).
- `TestProvider_Chat_NonStreamingSingleChunk_SatisfiesPort` (boundary — one line with content+done, proving the port's non-streaming contract).
- `TestProvider_Live_Chat_RealOllamaAnswers` (integration, `//go:build ollama_live`, opt-in).

---

## Task 9: Tool toolkit — arg-validation + path containment + result envelope

**Wave:** 1 · **blockedBy:** T2 · **PR:** small–medium. **Parallel-safe with T6, T7, T8, T10. Blocks T11–T14.**

The one shared package every tool depends on — **the single home of SR-5 (arg validation) and SR-2 (workspace path containment)**, plus the tool-result envelope convention (locked cross-cutting decision #3). Isolating it here is what keeps the four tools file-disjoint and each trivially small. It lives in `internal/adapter/tool/toolkit`; nothing outside the tools imports it.

**Files:**
- Create: `internal/adapter/tool/toolkit/validate.go` (`Validate`)
- Create: `internal/adapter/tool/toolkit/path.go` (`ResolvePath`, `ErrPathEscape`)
- Create: `internal/adapter/tool/toolkit/result.go` (`Err`, `OK`)
- Test: `internal/adapter/tool/toolkit/path_test.go`, `validate_test.go`, `result_test.go`

**Interfaces:**
- Consumes: `encoding/json`, `path/filepath`, `strings`, `github.com/go-playground/validator/v10`.
- Produces:
  - `func Validate(args json.RawMessage, dst any) error` — decodes with `DisallowUnknownFields`, then `validator.Struct(dst)`; returns a wrapped error (SR-5). Package-level `validator` singleton.
  - `func ResolvePath(workspaceRoot, argPath string) (string, error)` — SR-2 containment; returns the cleaned absolute path guaranteed inside `workspaceRoot`, or `ErrPathEscape`.
  - `var ErrPathEscape = errors.New("path escapes workspace")`
  - `func Err(format string, a ...any) json.RawMessage` → `{"error":"..."}`
  - `func OK(v any) json.RawMessage` → `{"ok":<v>}` (soft-result envelope, nil Go error at call site).

**ResolvePath algorithm (SR-2 — implement exactly):**
1. Reject if `argPath == ""` or `filepath.IsAbs(argPath)` → `ErrPathEscape`.
2. `joined := filepath.Join(workspaceRoot, argPath)` then `clean := filepath.Clean(joined)`.
3. Resolve symlinks defensively: `EvalSymlinks` on the longest existing ancestor of `clean` (so a not-yet-created write target still checks), re-join the non-existent remainder.
4. `rel, err := filepath.Rel(rootResolved, resolved)`; reject if `err != nil` or `rel == ".."` or `strings.HasPrefix(rel, ".."+string(os.PathSeparator))` → `ErrPathEscape`.
5. Return `resolved`.

- [ ] **Step 1: Write the path-containment tests (the security core of this slice)**

```go
func TestResolvePath_RelativeInsideRoot_Resolves(t *testing.T) {
	root := t.TempDir()
	got, err := ResolvePath(root, "sub/file.txt")
	if err != nil { t.Fatal(err) }
	if !strings.HasPrefix(got, root) { t.Fatalf("%q not under %q", got, root) }
}
func TestResolvePath_DotDotEscape_Rejected(t *testing.T) {
	if _, err := ResolvePath(t.TempDir(), "../../etc/passwd"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("want ErrPathEscape, got %v", err)
	}
}
func TestResolvePath_AbsolutePath_Rejected(t *testing.T) {
	if _, err := ResolvePath(t.TempDir(), "/etc/passwd"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("want ErrPathEscape, got %v", err)
	}
}
func TestResolvePath_SymlinkEscapingRoot_Rejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(root, "link")) // link -> outside root
	if _, err := ResolvePath(root, "link/secret"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("symlink escape not caught: %v", err)
	}
}
func TestResolvePath_EmptyPath_Rejected(t *testing.T) { /* "" ⇒ ErrPathEscape */ }
```

- [ ] **Step 2: Implement `path.go`** per the algorithm. **Step 3: Run — expect GREEN.**

- [ ] **Step 4: Write the validation tests**

```go
type sample struct{ Path string `json:"path" validate:"required"` }
func TestValidate_MissingRequiredField_ReturnsError(t *testing.T) {
	var s sample
	if err := Validate(json.RawMessage(`{}`), &s); err == nil { t.Fatal("want error for missing path") }
}
func TestValidate_UnknownField_Rejected(t *testing.T) {
	var s sample
	if err := Validate(json.RawMessage(`{"path":"x","evil":1}`), &s); err == nil { t.Fatal("want error for unknown field") }
}
func TestValidate_MalformedJSON_ReturnsErrorNoPanic(t *testing.T) {
	var s sample
	if err := Validate(json.RawMessage(`{not json`), &s); err == nil { t.Fatal("want error for malformed json") }
}
func TestValidate_ValidArgs_PopulatesStruct(t *testing.T) {
	var s sample
	if err := Validate(json.RawMessage(`{"path":"go.mod"}`), &s); err != nil || s.Path != "go.mod" {
		t.Fatalf("err=%v s=%+v", err, s)
	}
}
```

- [ ] **Step 5: Implement `validate.go` + `result.go`.** `Err`/`OK` marshal fixed envelopes. **Step 6: Run — expect GREEN.** Commit.

```bash
git add internal/adapter/tool/toolkit
git commit -m "feat(tool/toolkit): SR-2 path containment, SR-5 arg validation, result envelope"
```

**Acceptance criteria:**
- `ResolvePath` rejects `..` escapes, absolute paths, empty paths, and symlinks pointing outside root (SR-2) — with `ErrPathEscape`, never a panic.
- `Validate` rejects malformed JSON, missing required fields, and unknown fields cleanly (SR-5).
- `Err`/`OK` produce the frozen envelope shapes used by every tool.

**Test list (all unit):**
- Path: `TestResolvePath_RelativeInsideRoot_Resolves`, `TestResolvePath_DotDotEscape_Rejected`, `TestResolvePath_AbsolutePath_Rejected`, `TestResolvePath_SymlinkEscapingRoot_Rejected`, `TestResolvePath_EmptyPath_Rejected`.
- Validate: `TestValidate_MissingRequiredField_ReturnsError`, `TestValidate_UnknownField_Rejected`, `TestValidate_MalformedJSON_ReturnsErrorNoPanic`, `TestValidate_ValidArgs_PopulatesStruct`.
- Result: `TestErr_ProducesErrorEnvelope`, `TestOK_ProducesOKEnvelope`.

---

## Task 10: In-process ToolRegistry adapter

**Wave:** 1 · **blockedBy:** T3 · **PR:** small. **Parallel-safe with T6, T7, T8, T9.**

The map-backed `core.ToolRegistry` (spec decision 3, arch ADR-0002). It holds `core.Tool` values `cmd` passes in — it does **not** import the tool packages, so it never contends with T11–T14. Shaped so a future gRPC-plugin registry drops in behind the same interface.

**Files:**
- Create: `internal/adapter/tool/registry/registry.go`
- Test: `internal/adapter/tool/registry/registry_test.go`

**Interfaces:**
- Consumes: `core.Tool`, `core.ToolRegistry`, `core.ToolSchema` (T2/T3).
- Produces: `func New(tools ...core.Tool) *Registry` implementing `core.ToolRegistry`; duplicate names return an error (`func New(...) (*Registry, error)` — decide: return error on duplicate so `cmd` fails fast).

- [ ] **Step 1: Write failing tests**

```go
func TestRegistry_Get_RegisteredTool_ReturnsIt(t *testing.T) {
	tl := stubTool{name: "read"}
	r, _ := New(tl)
	got, ok := r.Get("read")
	if !ok || got.Schema().Name != "read" { t.Fatalf("get failed: %v %v", got, ok) }
}
func TestRegistry_Get_UnknownTool_ReturnsFalse(t *testing.T) {
	r, _ := New()
	if _, ok := r.Get("nope"); ok { t.Fatal("want ok=false for unknown tool") }
}
func TestRegistry_Schemas_ReturnsAllRegisteredSchemas(t *testing.T) {
	r, _ := New(stubTool{name: "read"}, stubTool{name: "write"})
	if len(r.Schemas()) != 2 { t.Fatalf("want 2 schemas, got %d", len(r.Schemas())) }
}
func TestRegistry_New_DuplicateNames_ReturnsError(t *testing.T) {
	if _, err := New(stubTool{name: "read"}, stubTool{name: "read"}); err == nil {
		t.Fatal("want error on duplicate tool name")
	}
}
```

- [ ] **Step 2: Implement `registry.go`** — `map[string]core.Tool`, `New` errors on duplicate `Schema().Name`, `Schemas()` returns a stable-ordered slice, `Get` is a map lookup. **Step 3: Run — expect GREEN.** Commit.

```bash
git add internal/adapter/tool/registry
git commit -m "feat(tool/registry): in-process map ToolRegistry"
```

**Acceptance criteria:**
- `Get` resolves by name (ok=false when absent); `Schemas` returns every registered schema; duplicate names rejected at construction. Does not import any tool package.

**Test list (all unit):**
- `TestRegistry_Get_RegisteredTool_ReturnsIt`, `TestRegistry_Get_UnknownTool_ReturnsFalse`, `TestRegistry_Schemas_ReturnsAllRegisteredSchemas`, `TestRegistry_New_DuplicateNames_ReturnsError`.

---

## Task 11: `read` tool

**Wave:** 2 · **blockedBy:** T9 · **PR:** small. **Parallel-safe with T12, T13, T14, T15.**

Reads a file inside the workspace. SR-2 (containment via `toolkit.ResolvePath`), SR-4b (byte cap + truncation notice), SR-5 (arg validation).

**Files:**
- Create: `internal/adapter/tool/read/read.go`
- Test: `internal/adapter/tool/read/read_test.go`

**Interfaces:**
- Consumes: `core.Tool`, `core.ToolSchema` (T2/T3); `toolkit.Validate`, `toolkit.ResolvePath`, `toolkit.Err`, `toolkit.OK` (T9).
- Produces: `func New(workspaceRoot string, maxBytes int64) core.Tool`. Schema name `"read"`, params `{path: string(required)}`. `Invoke` returns `{"ok":{"content":..., "truncated":bool}}` or `{"error":...}` (nil Go error for soft failures).

- [ ] **Step 1: Write tests**

```go
func TestRead_ExistingFileInWorkspace_ReturnsContent(t *testing.T) {
	root := t.TempDir(); os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o644)
	res, err := New(root, 1<<20).Invoke(ctx, json.RawMessage(`{"path":"a.txt"}`))
	if err != nil { t.Fatal(err) }
	if !strings.Contains(string(res), "hello") { t.Fatalf("res=%s", res) }
}
func TestRead_PathEscape_ReturnsErrorEnvelopeNilGoError(t *testing.T) {
	res, err := New(t.TempDir(), 1<<20).Invoke(ctx, json.RawMessage(`{"path":"../../etc/passwd"}`))
	if err != nil { t.Fatalf("want soft error, got Go error %v", err) }
	if !strings.Contains(string(res), "error") { t.Fatalf("want error envelope, got %s", res) }
}
func TestRead_FileLargerThanCap_TruncatesAndFlags(t *testing.T) {
	root := t.TempDir(); os.WriteFile(filepath.Join(root, "big"), bytes.Repeat([]byte("x"), 100), 0o644)
	res, _ := New(root, 10).Invoke(ctx, json.RawMessage(`{"path":"big"}`))
	if !strings.Contains(string(res), `"truncated":true`) { t.Fatalf("want truncated flag, got %s", res) }
}
func TestRead_MissingFile_ReturnsErrorEnvelope(t *testing.T) { /* nonexistent path ⇒ {"error":...}, nil Go error */ }
func TestRead_MalformedArgs_ReturnsErrorEnvelope(t *testing.T) { /* {"path":123} ⇒ error envelope */ }
```

- [ ] **Step 2: Implement `read.go`** — `Invoke`: `toolkit.Validate` into `struct{Path string}`; `toolkit.ResolvePath` (escape ⇒ `toolkit.Err`, nil Go error); open + read up to `maxBytes+1` to detect overflow (SR-4b), truncate to `maxBytes`, set `truncated`. I/O error (missing/perm) ⇒ `toolkit.Err`. **Step 3: GREEN.** Commit.

```bash
git add internal/adapter/tool/read
git commit -m "feat(tool/read): workspace-scoped read with byte cap"
```

**Acceptance criteria:** reads inside workspace; SR-2 escape, missing file, and malformed args all return the error envelope with **nil** Go error; SR-4b cap truncates with a flag.

**Test list (all unit):** `TestRead_ExistingFileInWorkspace_ReturnsContent`, `TestRead_PathEscape_ReturnsErrorEnvelopeNilGoError`, `TestRead_FileLargerThanCap_TruncatesAndFlags`, `TestRead_MissingFile_ReturnsErrorEnvelope`, `TestRead_MalformedArgs_ReturnsErrorEnvelope`, `TestRead_Schema_AdvertisesPathParam`.

---

## Task 12: `write` tool

**Wave:** 2 · **blockedBy:** T9 · **PR:** small. **Parallel-safe with T11, T13, T14, T15.**

Writes/creates a file inside the workspace. SR-2, SR-5.

**Files:**
- Create: `internal/adapter/tool/write/write.go`
- Test: `internal/adapter/tool/write/write_test.go`

**Interfaces:**
- Consumes: `core.Tool` (T2/T3); `toolkit.*` (T9).
- Produces: `func New(workspaceRoot string) core.Tool`. Schema `"write"`, params `{path: string(required), content: string(required)}`. `Invoke` returns `{"ok":{"bytes":N}}` or `{"error":...}`.

- [ ] **Step 1: Write tests**

```go
func TestWrite_NewFileInWorkspace_PersistsContent(t *testing.T) {
	root := t.TempDir()
	res, err := New(root).Invoke(ctx, json.RawMessage(`{"path":"out.txt","content":"data"}`))
	if err != nil { t.Fatal(err) }
	b, _ := os.ReadFile(filepath.Join(root, "out.txt"))
	if string(b) != "data" { t.Fatalf("file=%q res=%s", b, res) }
}
func TestWrite_PathEscape_RejectedNoWriteOutsideRoot(t *testing.T) {
	root := t.TempDir()
	res, err := New(root).Invoke(ctx, json.RawMessage(`{"path":"../evil.txt","content":"x"}`))
	if err != nil { t.Fatal(err) }
	if !strings.Contains(string(res), "error") { t.Fatalf("want error, got %s", res) }
	if _, e := os.Stat(filepath.Join(filepath.Dir(root), "evil.txt")); e == nil {
		t.Fatal("wrote outside workspace — SR-2 breach")
	}
}
func TestWrite_CreatesParentDirsInsideRoot(t *testing.T) { /* path "a/b/c.txt" ⇒ mkdir -p under root */ }
func TestWrite_MissingContentArg_ReturnsErrorEnvelope(t *testing.T) { /* SR-5 */ }
```

- [ ] **Step 2: Implement `write.go`** — validate, resolve (escape ⇒ envelope + assert no write), `os.MkdirAll` the resolved parent (still inside root), `os.WriteFile` 0o644. **Step 3: GREEN.** Commit.

```bash
git add internal/adapter/tool/write
git commit -m "feat(tool/write): workspace-scoped write"
```

**Acceptance criteria:** writes inside workspace; SR-2 escape rejected **and no file created outside root**; missing args → envelope.

**Test list (all unit):** `TestWrite_NewFileInWorkspace_PersistsContent`, `TestWrite_PathEscape_RejectedNoWriteOutsideRoot`, `TestWrite_CreatesParentDirsInsideRoot`, `TestWrite_MissingContentArg_ReturnsErrorEnvelope`, `TestWrite_Schema_AdvertisesPathAndContent`.

---

## Task 13: `edit` tool

**Wave:** 2 · **blockedBy:** T9 · **PR:** small. **Parallel-safe with T11, T12, T14, T15.**

Replaces an exact substring in a workspace file (old→new). SR-2, SR-5. Soft errors: file missing, `old` not found, `old` non-unique.

**Files:**
- Create: `internal/adapter/tool/edit/edit.go`
- Test: `internal/adapter/tool/edit/edit_test.go`

**Interfaces:**
- Consumes: `core.Tool` (T2/T3); `toolkit.*` (T9).
- Produces: `func New(workspaceRoot string) core.Tool`. Schema `"edit"`, params `{path: string(required), old: string(required), new: string(required)}`. Returns `{"ok":{"replaced":1}}` or `{"error":...}`.

- [ ] **Step 1: Write tests**

```go
func TestEdit_UniqueOldString_ReplacesAndPersists(t *testing.T) {
	root := t.TempDir(); os.WriteFile(filepath.Join(root, "f"), []byte("foo bar"), 0o644)
	res, err := New(root).Invoke(ctx, json.RawMessage(`{"path":"f","old":"bar","new":"baz"}`))
	if err != nil { t.Fatal(err) }
	b, _ := os.ReadFile(filepath.Join(root, "f"))
	if string(b) != "foo baz" { t.Fatalf("file=%q res=%s", b, res) }
}
func TestEdit_OldStringNotFound_ReturnsErrorEnvelopeNoWrite(t *testing.T) { /* {"error":...}, file unchanged */ }
func TestEdit_OldStringNotUnique_ReturnsErrorEnvelope(t *testing.T) { /* "aa" in "aaaa" ⇒ ambiguous ⇒ error, no write */ }
func TestEdit_PathEscape_Rejected(t *testing.T) { /* SR-2 */ }
```

- [ ] **Step 2: Implement `edit.go`** — validate, resolve, read file; count occurrences of `old` (0 ⇒ "not found" envelope; >1 ⇒ "not unique" envelope; both leave file unchanged); replace the single occurrence, write back. **Step 3: GREEN.** Commit.

```bash
git add internal/adapter/tool/edit
git commit -m "feat(tool/edit): unique-substring edit within workspace"
```

**Acceptance criteria:** unique replace persists; not-found and non-unique return envelopes with the file **unchanged**; SR-2 escape rejected.

**Test list (all unit):** `TestEdit_UniqueOldString_ReplacesAndPersists`, `TestEdit_OldStringNotFound_ReturnsErrorEnvelopeNoWrite`, `TestEdit_OldStringNotUnique_ReturnsErrorEnvelope`, `TestEdit_PathEscape_Rejected`, `TestEdit_MissingFile_ReturnsErrorEnvelope`, `TestEdit_Schema_AdvertisesPathOldNew`.

---

## Task 14: `bash` tool

**Wave:** 2 · **blockedBy:** T9 · **PR:** medium. **Parallel-safe with T11, T12, T13, T15.**

Runs a shell command in a subprocess. **SR-3:** context timeout + fixed configured working directory + no inherited secrets beyond parent env (do not forward extra credentials). **SR-4c:** bounded stdout/stderr buffer, killed at timeout, output beyond cap truncated. SR-5 arg validation. The boundary is isolated so the future OS sandbox (SR-3a) drops in behind it.

**Files:**
- Create: `internal/adapter/tool/bash/bash.go`
- Test: `internal/adapter/tool/bash/bash_test.go`

**Interfaces:**
- Consumes: `core.Tool` (T2/T3); `toolkit.Validate`, `toolkit.Err`, `toolkit.OK` (T9); `os/exec`, `context`.
- Produces: `func New(workDir string, timeout time.Duration, maxOutputBytes int64) core.Tool`. Schema `"bash"`, params `{command: string(required)}`. Returns `{"ok":{"stdout":..., "stderr":..., "exit_code":N, "truncated":bool, "timed_out":bool}}`. A non-zero exit is a **soft** result (nil Go error) so the model sees it.

- [ ] **Step 1: Write tests**

```go
func TestBash_SimpleCommand_ReturnsStdoutAndZeroExit(t *testing.T) {
	res, err := New(t.TempDir(), 5*time.Second, 1<<20).Invoke(ctx, json.RawMessage(`{"command":"echo hi"}`))
	if err != nil { t.Fatal(err) }
	if !strings.Contains(string(res), "hi") || !strings.Contains(string(res), `"exit_code":0`) {
		t.Fatalf("res=%s", res)
	}
}
func TestBash_NonZeroExit_ReturnedAsSoftResultNotGoError(t *testing.T) {
	res, err := New(t.TempDir(), 5*time.Second, 1<<20).Invoke(ctx, json.RawMessage(`{"command":"exit 3"}`))
	if err != nil { t.Fatalf("want soft result, got Go error %v", err) }
	if !strings.Contains(string(res), `"exit_code":3`) { t.Fatalf("res=%s", res) }
}
func TestBash_CommandExceedsTimeout_KillsProcessAndFlagsTimedOut(t *testing.T) {
	start := time.Now()
	res, err := New(t.TempDir(), 200*time.Millisecond, 1<<20).Invoke(ctx, json.RawMessage(`{"command":"sleep 10"}`))
	if err != nil { t.Fatal(err) }
	if time.Since(start) > 3*time.Second { t.Fatal("process not killed at timeout — SR-3 breach") }
	if !strings.Contains(string(res), `"timed_out":true`) { t.Fatalf("res=%s", res) }
}
func TestBash_RunsInConfiguredWorkDir(t *testing.T) {
	dir := t.TempDir()
	res, _ := New(dir, 5*time.Second, 1<<20).Invoke(ctx, json.RawMessage(`{"command":"pwd"}`))
	if !strings.Contains(string(res), dir) { t.Fatalf("cwd not honored: %s", res) }
}
func TestBash_OutputExceedsCap_TruncatesAndFlags(t *testing.T) {
	res, _ := New(t.TempDir(), 5*time.Second, 16).Invoke(ctx, json.RawMessage(`{"command":"yes | head -c 100000"}`))
	if !strings.Contains(string(res), `"truncated":true`) { t.Fatalf("res=%s", res) }
}
func TestBash_MalformedArgs_ReturnsErrorEnvelope(t *testing.T) { /* SR-5 */ }
```

- [ ] **Step 2: Implement `bash.go`** — validate into `struct{Command string}`; derive `exec.CommandContext(ctx2, "bash", "-c", cmd)` where `ctx2 = context.WithTimeout(ctx, timeout)`; set `cmd.Dir = workDir` (SR-3b); `cmd.Env = os.Environ()` only — **do not append extra secrets** (SR-3c); capture stdout/stderr into bounded `*limitedBuffer` capping at `maxOutputBytes` and flagging truncation (SR-4c); on `ctx2.Err()==DeadlineExceeded` flag `timed_out:true` (the CommandContext kill happens automatically). Extract exit code via `*exec.ExitError`. Return the soft-result envelope; reserve a Go error only for exec-launch failure (e.g. bash not found). **Step 3: GREEN.** Commit.

```bash
git add internal/adapter/tool/bash
git commit -m "feat(tool/bash): bounded subprocess (SR-3 timeout/workdir/env, SR-4c caps)"
```

**Acceptance criteria:** command runs in the configured workdir with only the parent env; timeout kills the process and flags `timed_out`; output beyond the cap is truncated and flagged; non-zero exit is a soft result the model sees; malformed args → envelope. Boundary isolated for the future OS sandbox (SR-3a follow-up).

**Test list (all unit — real subprocess, but self-contained/fast, no external services):** `TestBash_SimpleCommand_ReturnsStdoutAndZeroExit`, `TestBash_NonZeroExit_ReturnedAsSoftResultNotGoError`, `TestBash_CommandExceedsTimeout_KillsProcessAndFlagsTimedOut`, `TestBash_RunsInConfiguredWorkDir`, `TestBash_OutputExceedsCap_TruncatesAndFlags`, `TestBash_MalformedArgs_ReturnsErrorEnvelope`, `TestBash_Schema_AdvertisesCommandParam`.

---

## Task 15: TUI adapter (Bubble Tea) + SR-1 sanitizer

**Wave:** 2 · **blockedBy:** T4, T6 · **PR:** medium. **Parallel-safe with T11, T12, T13, T14.**

The Bubble Tea Model: input box (Bubbles `textarea`/`textinput`), scrolling streaming viewport, status line (Lip Gloss). It calls `Agent.Send` and renders the `AgentEvent` stream — depending **only on core** (`Agent`, `AgentEvent`), never on `Provider`. **SR-1: all model text and all tool output are sanitized before render** — strip C0/C1 control bytes and ANSI/OSC escape sequences so untrusted content cannot hijack the terminal. Only the TUI's own Lip Gloss styling is applied.

**Files:**
- Create: `internal/adapter/tui/sanitize.go` (SR-1)
- Create: `internal/adapter/tui/model.go` (Bubble Tea `Model`, `Update`, `View`)
- Create: `internal/adapter/tui/program.go` (`NewProgram(a *core.Agent, sessionID string) *tea.Program`)
- Test: `internal/adapter/tui/sanitize_test.go`, `internal/adapter/tui/model_test.go`

**Interfaces:**
- Consumes: `core.Agent`, `core.AgentEvent`, `core.AgentEventType` (T4/T6); `charmbracelet/bubbletea`, `bubbles/textarea`, `bubbles/viewport`, `charmbracelet/lipgloss`.
- Produces:
  - `func Sanitize(s string) string` — strips control/escape bytes (SR-1).
  - `func NewModel(a *core.Agent, sessionID string) Model` implementing `tea.Model`.
  - `func NewProgram(a *core.Agent, sessionID string, opts ...tea.ProgramOption) *tea.Program`.

**Event bridge:** on user submit, `Update` fires a `tea.Cmd` that calls `a.Send` and returns a `tea.Msg` per `AgentEvent` (a small goroutine feeds a `tea.Msg` channel via `tea.Cmd` re-subscription). Each rendered chunk passes through `Sanitize` before it touches the viewport (streaming responsiveness NFR: render on each event, no batching).

- [ ] **Step 1: Write the SR-1 sanitizer tests (security core of the render boundary)**

```go
func TestSanitize_ANSIColorSequence_Stripped(t *testing.T) {
	if got := Sanitize("\x1b[31mred\x1b[0m"); got != "red" { t.Fatalf("got %q", got) }
}
func TestSanitize_OSC52ClipboardSequence_Stripped(t *testing.T) {
	in := "\x1b]52;c;ZXZpbA==\x07steal" // OSC 52 clipboard write
	if got := Sanitize(in); strings.Contains(got, "\x1b]52") { t.Fatalf("OSC 52 survived: %q", got) }
}
func TestSanitize_C0ControlBytes_Stripped(t *testing.T) {
	if got := Sanitize("a\x00b\x07c"); got != "abc" { t.Fatalf("got %q", got) }
}
func TestSanitize_PreservesNewlinesAndTabs(t *testing.T) {
	if got := Sanitize("line1\nline2\tend"); got != "line1\nline2\tend" { t.Fatalf("got %q", got) }
}
func TestSanitize_PlainText_Unchanged(t *testing.T) { /* "hello world" ⇒ unchanged */ }
```

- [ ] **Step 2: Implement `sanitize.go`** — a state machine (or vetted regex over ANSI CSI/OSC + a control-byte filter) that removes ESC-introduced sequences and C0/C1 controls **except** `\n` and `\t`. **Step 3: GREEN.**

- [ ] **Step 4: Write the model tests (unit, drive `Update` directly)**

```go
func TestModel_TextDeltaMsg_AppendsSanitizedTextToViewport(t *testing.T) {
	m := NewModel(nil, "s1")
	m2, _ := m.Update(agentEventMsg{ev: core.AgentEvent{Type: core.EventTextDelta, TextDelta: "\x1b[31mhi\x1b[0m"}})
	if !strings.Contains(m2.(Model).transcript(), "hi") || strings.Contains(m2.(Model).transcript(), "\x1b") {
		t.Fatalf("delta not sanitized/appended: %q", m2.(Model).transcript())
	}
}
func TestModel_ErrorMsg_ShowsErrorAndStaysUsable(t *testing.T) {
	m := NewModel(nil, "s1")
	m2, _ := m.Update(agentEventMsg{ev: core.AgentEvent{Type: core.EventError, Err: errors.New("ollama down")}})
	if !strings.Contains(m2.(Model).status(), "ollama down") { t.Fatal("error not surfaced") }
	// model is not quit
	if _, ok := m2.(Model). Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")}); !ok { /* still accepts input */ }
}
func TestModel_ToolCallStartedMsg_ShowsToolActivity(t *testing.T) { /* status shows the tool name */ }
```

- [ ] **Step 5: Implement `model.go` + `program.go`.** **Step 6: GREEN.** Commit.

```bash
git add internal/adapter/tui go.mod go.sum
git commit -m "feat(tui): Bubble Tea model, AgentEvent render, SR-1 sanitizer"
```

**Acceptance criteria:** SR-1 sanitizer strips ANSI/OSC/control bytes (keeps `\n`/`\t`) and is applied to every rendered delta and tool result; deltas render incrementally (no batching); an `EventError` surfaces and the TUI stays usable (graceful Ollama-down NFR). TUI imports core only — not `Provider`.

**Test list:**
- Sanitizer (unit): `TestSanitize_ANSIColorSequence_Stripped`, `TestSanitize_OSC52ClipboardSequence_Stripped`, `TestSanitize_C0ControlBytes_Stripped`, `TestSanitize_PreservesNewlinesAndTabs`, `TestSanitize_PlainText_Unchanged`.
- Model (unit): `TestModel_TextDeltaMsg_AppendsSanitizedTextToViewport`, `TestModel_ErrorMsg_ShowsErrorAndStaysUsable`, `TestModel_ToolCallStartedMsg_ShowsToolActivity`, `TestModel_ToolCallFinishedMsg_SanitizesToolResult`.

---

## Task 16: `cmd/pythia` composition root (DI wiring)

**Wave:** 3 · **blockedBy:** T5, T6, T7, T8, T10, T11, T12, T13, T14, T15 · **PR:** medium. **Parallel-safe with T17.**

The single place every adapter meets. Loads `Config`, opens SQLite + migrates, constructs the Ollama provider, builds the four tools + registry, constructs the `Agent`, resolves-or-creates the session, and runs the TUI program. **This is the only file that imports every package** — by design it conflicts with nothing until now. **WRONG if any wiring logic leaks into `internal/core`.**

**Files:**
- Create: `cmd/pythia/main.go`
- Test: `cmd/pythia/main_test.go` (a `run(cfg) error`-style seam tested with a temp DB + no TTY, asserting wiring succeeds and a session is created)

**Interfaces:**
- Consumes: `config.Load`; `sqlite.New`; `ollama.New`; `read.New`/`write.New`/`edit.New`/`bash.New`; `registry.New`; `core.NewAgent`, `core.WithMaxIterations`; `tui.NewProgram`.
- Produces: `func main()`; extract a testable `func run(cfg config.Config) error` that wires everything and starts the program (so wiring is unit-testable without a TTY — the `tea.Program` start is guarded behind an interface or skipped in test via a `--no-tui` bootstrap check).

**Startup order (matches < 200 ms cold-start NFR — no network at startup):** `config.Load` → `sqlite.New` (opens + migrates) → build tools+registry → `ollama.New` (lazy; no connection yet) → resolve session (`cfg.SessionID` empty ⇒ `NewID` + `CreateSession`; else `GetSession`, creating if missing) → `core.NewAgent(prov, reg, repo, WithMaxIterations(cfg.MaxIterations))` → `tui.NewProgram(agent, sessionID).Run()`.

- [ ] **Step 1: Write the wiring test**

```go
func TestRun_WithTempConfig_WiresAdaptersAndEnsuresSession(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		OllamaBaseURL: "http://localhost:11434", OllamaModel: "qwen3.5",
		WorkspaceRoot: dir, DBPath: filepath.Join(dir, "p.db"),
		BashTimeout: 5 * time.Second, MaxReadBytes: 1 << 20, MaxBashOutputBytes: 1 << 20,
		MaxIterations: 10, SessionID: "test-session",
	}
	// bootstrap only (no TTY): construct wiring, ensure the session, return before program.Run()
	if err := bootstrap(cfg); err != nil { t.Fatalf("bootstrap: %v", err) }
	r, _ := sqlite.New(cfg.DBPath); defer r.Close()
	if _, err := r.GetSession(context.Background(), "test-session"); err != nil {
		t.Fatalf("session not created by bootstrap: %v", err)
	}
}
```

- [ ] **Step 2: Implement `main.go`** with `main()` → `config.Load()` → `run(cfg)`, and a `bootstrap(cfg)` that does all wiring + session-ensure and returns the constructed `*tea.Program` (so the test calls `bootstrap`, `main`/`run` calls `bootstrap` then `.Run()`). No content logged (SR-7). **Step 3: GREEN.**

- [ ] **Step 4: Manual acceptance check** (documented, run once by the executor): `CGO_ENABLED=0 go build ./...` → run binary against a real Ollama → prompt "what's in go.mod?" triggers a `read` tool call and an answer → prompt writing a file persists on disk → relaunch with the same `PYTHIA_SESSION_ID` replays history → tokens stream. (These are the spec's acceptance criteria; the automated coverage is T17.)

- [ ] **Step 5: Commit**

```bash
git add cmd/pythia
git commit -m "feat(cmd/pythia): composition root wiring all adapters via DI"
```

**Acceptance criteria:** `CGO_ENABLED=0 go build ./...` yields one binary; `bootstrap` wires every adapter and ensures the session; startup does no network I/O (Ollama lazy); no content logged.

**Test list:**
- `TestRun_WithTempConfig_WiresAdaptersAndEnsuresSession` (integration — real SQLite, no TTY).
- `TestBootstrap_MissingSessionID_CreatesNewSession` (integration).
- `TestBootstrap_ExistingSessionID_ReusesIt` (integration — resume path).

---

## Task 17: e2e TUI journey (teatest)

**Wave:** 3 · **blockedBy:** T6, T15 · **PR:** small–medium. **Parallel-safe with T16.**

The golden-frame / interaction e2e (arch doc §5, stack profile: `teatest`). Drives the real `tea.Program` with a **stub Provider** (scripted stream incl. a tool round-trip) + an in-memory-or-temp repo + the real registry/tools, asserting: streamed output renders incrementally, a tool call round-trips, and untrusted escape sequences are sanitized on screen (SR-1 end-to-end). First step verifies the current teatest import path (open question from the spec).

**Files:**
- Create: `internal/adapter/tui/e2e_test.go`

**Interfaces:**
- Consumes: `tui.NewProgram`, `core.NewAgent`, the stub `Provider` from the core test helpers (or a local scripted provider), `charmbracelet/x/exp/teatest`.

- [ ] **Step 1: Verify + get the teatest import path**

```bash
go get github.com/charmbracelet/x/exp/teatest@latest
```
Confirm the package path resolves; record it in the test import.

- [ ] **Step 2: Write the e2e streaming + tool round-trip test**

```go
func TestTUI_UserPrompt_StreamsAnswerAndRoundsTripToolCall(t *testing.T) {
	repo := newTempRepo(t) // real sqlite or in-memory core fake; session "s1"
	prov := scriptedProvider( /* turn1: tool_call read; turn2: "the module is pythia"+done */ )
	reg := registryWithRealReadTool(t)
	agent := core.NewAgent(prov, reg, repo)
	tm := teatest.NewTestModel(t, tui.NewModel(agent, "s1"), teatest.WithInitialTermSize(80, 24))
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("what's the module?")})
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("the module is pythia"))
	}, teatest.WithDuration(3*time.Second))
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}
func TestTUI_ProviderEmitsEscapeSequence_RendersInert(t *testing.T) {
	// scripted provider streams "\x1b]52;c;ZXZpbA==\x07" ; assert it never appears raw in Output()
}
func TestTUI_OllamaDown_ShowsErrorStaysUsable(t *testing.T) {
	// provider returns setup error ; assert error text on screen and program not finished
}
```

- [ ] **Step 3: Run — expect GREEN** (`go test ./internal/adapter/tui/...`). Commit.

```bash
git add internal/adapter/tui/e2e_test.go go.mod go.sum
git commit -m "test(tui): teatest e2e — streaming, tool round-trip, SR-1, Ollama-down"
```

**Acceptance criteria:** e2e proves a full user journey (type → stream → tool round-trip → answer) against a stub Provider; SR-1 escapes render inert end-to-end; Ollama-down shows an error and the TUI stays usable. No live Ollama required (deterministic).

**Test list (all e2e, teatest):**
- `TestTUI_UserPrompt_StreamsAnswerAndRoundsTripToolCall`
- `TestTUI_ProviderEmitsEscapeSequence_RendersInert` (SR-1 end-to-end)
- `TestTUI_OllamaDown_ShowsErrorStaysUsable` (graceful-degrade NFR)

---

## Self-Review (spec coverage / placeholder / type consistency)

**Spec coverage** — every spec item maps to a task:
- Single `go build` CGO-free binary → T1 (fitness) + T16 (build) + `make check-cgo`.
- Bubble Tea TUI (input/streaming/status) → T15; streaming render → T15 + T8.
- Agent turn loop w/ tool dispatch + bounded iterations → T6 (SR-4a).
- 4 tools via one `ToolRegistry` → T10 + T11–T14.
- `Provider` port, Ollama streaming impl → T3 + T8.
- SQLite persistence + repository + migrations → T3 + T7; resume across restart → T7 + T16.
- Tool-arg validation at adapter boundary → T9 + each tool (SR-5).
- Unit/integration/e2e tiers → present per task; e2e → T17.
- "Swap Provider / add a tool with no core change" → guaranteed by T3 ports + T1 fitness test (verified by interfaces, not a second impl).
- All open questions routed: Ollama wire format → T8 (faked + opt-in live); teatest import path → T17 step 1; message-schema single-table → T7 (per data doc, already resolved).

**Security coverage** — SR-1 → T15; SR-2 → T9 (+ T11/T12/T13); SR-3/3b/3c → T14; SR-4a → T6; SR-4b → T11; SR-4c → T14; SR-5 → T9 (+ every tool); SR-6 → T7; SR-7 → T7/T16.

**Type consistency** — port signatures, `AgentEvent` variants, `NewID`, `toolkit.{Validate,ResolvePath,Err,OK}`, and the `{"ok"|"error"}` envelope are used identically across all tasks that reference them; frozen in T2/T3/T4/T9 and consumed unchanged downstream.

**Dependency-rule guard** — T6 (turn loop) is the one core task with real logic; its tests use fakes and it imports only stdlib + core. The T1 fitness test fails any regression. No task makes `internal/core` import an adapter.
