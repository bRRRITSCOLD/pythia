# 0003 — SQLite via modernc.org/sqlite behind a SessionRepository boundary

**Status:** Accepted

## Context

A session and its full message history (including tool calls and results) must
persist so that killing and relaunching the binary with the same session id
replays prior history (spec; acceptance criterion). Two independent decisions:
**which store** and **how core sees it**.

Store selection — the overriding constraint is the single-binary, CGO-free
discipline (stack profile: `go build` must yield one portable binary):

| Option | Strengths | Weaknesses | When to prefer |
|--------|-----------|------------|----------------|
| **`modernc.org/sqlite` (pure Go)** (chosen) | CGO-free → preserves single portable binary; real SQL/transactions; embedded, no server; file is easy to inspect/back up | Slightly slower than the C build; pure-Go SQLite is younger than the C original | Local embedded store that must ship CGO-free — our exact case |
| **`mattn/go-sqlite3` (cgo)** | Canonical, fastest SQLite | Requires CGO → breaks the single-binary/cross-compile discipline | When CGO is already accepted |
| **Plain JSON/append-only file** | Zero deps | No query/transaction story; concurrent-write and partial-write hazards; reinvents a DB | Trivial config, not conversation history |

Boundary — core must never touch SQL (spec resolved decision 6). A repository
port keeps the domain free of persistence concerns and keeps the store
swappable/testable.

## Decision

Persist to embedded SQLite via **`modernc.org/sqlite`** (pure Go, CGO-free),
behind a `SessionRepository` port defined in `internal/core`:

```go
type SessionRepository interface {
	CreateSession(ctx context.Context, s Session) error
	GetSession(ctx context.Context, id string) (Session, error)
	AppendMessage(ctx context.Context, m Message) error
	Messages(ctx context.Context, sessionID string) ([]Message, error)
}
```

The SQLite adapter (`internal/adapter/store/sqlite`) implements the port, owns
schema migrations, and uses **parameterized queries exclusively** (SR-6). Core
uses only the port and the domain types; it never imports the driver or writes
SQL. Messages are appended as they are produced (committed per message), so a
process kill mid-turn leaves valid, resumable history. `GetSession` returns
`core.ErrSessionNotFound` when absent — the port's error vocabulary is core's,
not the driver's.

The exact message table shape (typed columns vs. a content-blocks table) is a
data-phase decision **behind this port**; it does not affect core.

## Consequences

- **Easier:** single portable binary is preserved (CGO-free); the DB file is a
  standard SQLite file, trivially inspectable and backup-able.
- **Easier:** core is testable with an in-memory fake repository; the port
  contract test (incl. `ErrSessionNotFound`) guards behavior across impls.
- **Easier:** a future store swap (or adding chromem-go for memory) is a new
  adapter, not a core change.
- **Harder:** pure-Go SQLite is marginally slower than the cgo build — acceptable
  for a single-user local tool where the DB is not the bottleneck.
- **Obligation:** the adapter owns migrations and must translate driver errors
  into the port's documented errors so the driver never leaks into core.
