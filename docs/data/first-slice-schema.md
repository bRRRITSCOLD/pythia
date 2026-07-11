# Pythia — First Slice Persistence Schema

**Status:** Accepted for build · **Scope:** two aggregates only (Session + its Message history)
**Skill:** `compainy:data-modeling` (access-pattern-first, aggregate boundaries, KISS/YAGNI)
**Store:** SQLite (embedded) via `modernc.org/sqlite` — pure Go, CGO-free (per [`.ai/stack-profile.md`](../../.ai/stack-profile.md), [ADR-0003](../adr/0003-sqlite-modernc-repository-boundary.md))
**Behind:** the `SessionRepository` port in `internal/core/session.go` — core never sees SQL (SR-6)

This document is the contract the `internal/adapter/store/sqlite` adapter binds to.
It designs the persistence layer for exactly the two aggregates in this slice and
nothing more. The store selection is already settled by ADR-0003; this document
designs the schema, indexes, migration mechanism, and connection setup behind that
port.

---

## 1. Aggregates and store mapping

Two aggregates, one transactional boundary each (DDD: one aggregate = one write
transaction):

| Aggregate | Root | Owned entities | Store shape |
|---|---|---|---|
| **Session** | `Session` | — | one `sessions` row |
| **Message history** | the ordered `[]Message` for a session | `ToolCall`s (embedded) | N `messages` rows |

`ToolCall` is **not** an independent aggregate. It has no identity or lifecycle of
its own outside the assistant `Message` that emitted it — it is always written with
that message and always read back with it. Per the document-modeling rule (embed
children read together and never queried in isolation), tool calls are embedded in
their message row as JSON, not normalized into a child table.

The two aggregates are written independently (`CreateSession` then many
`AppendMessage`), consistent with the port: a session is created once, messages are
appended one at a time and committed as produced so a mid-turn process kill leaves
valid, resumable history.

---

## 2. The real access patterns (schema derives from these)

The `SessionRepository` port issues exactly four operations, which reduce to four
queries. The schema is designed for these and no others (YAGNI — no speculative
shape, no speculative index):

| Port op | Query | Frequency |
|---|---|---|
| `CreateSession` | `INSERT` one session row | once per session |
| `GetSession` | `SELECT ... FROM sessions WHERE id = ?` | on resume / per turn |
| `AppendMessage` | `INSERT` one message row (seq auto-assigned) | hot — every user/assistant/tool turn |
| `Messages` | `SELECT ... FROM messages WHERE session_id = ? ORDER BY seq` | on resume + to build each `ChatRequest` |

No query filters messages by role, by tool_call_id, or by content; nothing joins
across sessions; nothing reads a tool call independently of its message. That is the
whole justification for the design below.

---

## 3. Table design decision — single `messages` table with JSON tool_calls

**Resolved open question (spec):** single `messages` table with typed columns + a
JSON column for structured `tool_calls`. **Not** a separate content-blocks /
tool_calls table.

Justification (KISS + the actual query pattern):

- The two message queries are "append one message" and "load the full ordered
  history for one session." A single wide row serves both directly: `AppendMessage`
  is exactly one `INSERT`; `Messages` is one indexed range scan with no join.
- A separate `tool_calls` (or content-blocks) table buys nothing here. It would
  force a join on every history load, turn each append into a multi-statement write,
  and add a table — all to normalize data that is never queried on its own. That is
  complexity with no read or integrity dividend.
- Tool calls are a bounded, small, write-once list on assistant turns. JSON is the
  correct representation for an embedded value object that travels with its parent.
- The `Message` domain struct is already a single struct carrying all roles (unused
  fields zero per role); the single-table row is its natural 1:1 persistence image.

If a future slice needs to query tool calls independently (e.g. analytics over tool
usage), that is a new access pattern that justifies normalization then — reversible
behind the port, invisible to core. Not now.

---

## 4. DDL

Both tables are `STRICT` (SQLite ≥ 3.37, satisfied by modernc's bundled engine) so
column types are actually enforced at the storage layer, not just declared —
integrity at the lowest cheap level, per the skill. Datetimes are stored as
RFC3339Nano UTC text: inspectable, lexicographically sortable, and a clean
`time.Time` round-trip. (Timestamps are audit/display data — they are **not** the
message ordering key; see §5.)

```sql
CREATE TABLE sessions (
    id         TEXT PRIMARY KEY NOT NULL,   -- caller/adapter-assigned session id
    title      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,               -- RFC3339Nano UTC
    updated_at TEXT NOT NULL                -- RFC3339Nano UTC
) STRICT;

CREATE TABLE messages (
    id           TEXT PRIMARY KEY NOT NULL, -- stable message id (adapter- or core-assigned)
    session_id   TEXT NOT NULL,             -- owning session (aggregate root)
    seq          INTEGER NOT NULL,          -- monotonic, gapless, per-session replay order
    role         TEXT NOT NULL
                 CHECK (role IN ('system','user','assistant','tool')),
    content      TEXT NOT NULL DEFAULT '',  -- text; '' for a tool-calls-only assistant turn
    tool_calls   TEXT                       -- JSON array of ToolCall; NULL when none
                 CHECK (tool_calls IS NULL OR json_valid(tool_calls)),
    tool_call_id TEXT,                      -- set only for role='tool'; NULL otherwise
    created_at   TEXT NOT NULL,             -- RFC3339Nano UTC (audit, NOT ordering key)
    FOREIGN KEY (session_id) REFERENCES sessions(id),
    UNIQUE (session_id, seq)                -- ordering integrity + serves the history read
) STRICT;
```

Column / constraint rationale:

- **`role` CHECK** — enforces the four `core.Role` values at the schema, mirroring the
  domain enum. Defense-in-depth over the adapter's own mapping.
- **`content NOT NULL DEFAULT ''`** — the domain models absence of text as an empty
  string, not NULL; the column matches, so reads never deal with NULL text.
- **`tool_calls` JSON + `json_valid` CHECK** — the column is NULL when a message has no
  tool calls (user, system, tool, and plain assistant turns), and a validated JSON
  array otherwise. The CHECK guarantees we never persist a malformed blob.
- **`tool_call_id`** — a scalar correlation id, first-class on tool-role messages, so it
  is its own column rather than buried in JSON. NULL for every non-tool role.
- **`FOREIGN KEY (session_id)`** — a message cannot exist without its session (requires
  `PRAGMA foreign_keys = ON`; see §8).
- **`UNIQUE (session_id, seq)`** — enforces that ordering is unambiguous and does
  double duty as the history-read index (see §6 and §7).

---

## 5. Ordering guarantee — explicit monotonic `seq` per session

**Chosen:** an explicit `INTEGER seq`, monotonic and gapless **within a session**,
assigned at insert time. Rejected alternatives:

- **`created_at`** — sub-millisecond appends (assistant delta commit, then tool result)
  can collide on the same timestamp, and a non-monotonic wall clock (NTP step) can
  even invert order. It cannot guarantee stable replay order. Kept only as audit data.
- **`rowid`** — would order correctly (insert order is global-monotonic), but it ties
  replay order to a storage-implementation identity rather than an explicit domain
  ordering, and it is per-table-global rather than per-session. `seq` makes the
  session-scoped ordering an explicit, inspectable invariant that the `UNIQUE`
  constraint protects.

`seq` is assigned **inside the append `INSERT`** with a correlated subquery, so it is
atomic and needs no read round-trip:

```sql
INSERT INTO messages
    (id, session_id, seq, role, content, tool_calls, tool_call_id, created_at)
VALUES
    (?, ?,
     (SELECT COALESCE(MAX(seq), 0) + 1 FROM messages WHERE session_id = ?),
     ?, ?, ?, ?, ?);
```

This is parameterized (SR-6). With the single-writer connection setup (§8) the
`MAX(seq)+1` computation is race-free; the `UNIQUE (session_id, seq)` constraint is
the belt-and-suspenders guard that a duplicate ordering can never be committed even
if that assumption is ever violated. History replay is then simply:

```sql
SELECT id, session_id, seq, role, content, tool_calls, tool_call_id, created_at
FROM messages
WHERE session_id = ?
ORDER BY seq;
```

---

## 6. Indexes — exactly two constraint-backed, zero speculative

Per the skill, an index is added only when a hot-path query would otherwise scan or
a constraint requires it. Both indexes here are created implicitly by the
constraints above — there is nothing extra to add:

| Index | Backing | Serves | Justified by |
|---|---|---|---|
| PK on `sessions.id` | `PRIMARY KEY` | `GetSession` point lookup | identity + the point read |
| unique on `messages(session_id, seq)` | `UNIQUE (session_id, seq)` | `Messages` filter+`ORDER BY seq` | ordering integrity **and** the hot history read |

The `messages` PK on `id` exists for identity, not for a query in this slice. The
composite unique index is the one that matters for reads: `WHERE session_id = ?
ORDER BY seq` is a covered index range scan — no sort step, no table-scan. No index
on `role`, `tool_call_id`, or `created_at`: no query filters on them (YAGNI).

---

## 7. Domain ↔ row mapping

The row is a 1:1 image of `core.Message` / `core.Session`. Mapping the adapter owns
(core never sees any of this):

### `core.Session` ↔ `sessions`

| Domain field | Column | Transform |
|---|---|---|
| `ID string` | `id` | direct |
| `Title string` | `title` | direct |
| `CreatedAt time.Time` | `created_at` | `t.UTC().Format(time.RFC3339Nano)` / parse back |
| `UpdatedAt time.Time` | `updated_at` | same |

### `core.Message` ↔ `messages`

| Domain field | Column | Transform |
|---|---|---|
| `ID string` | `id` | direct |
| `SessionID string` | `session_id` | direct |
| — | `seq` | adapter-assigned at insert (§5); not surfaced on the domain struct |
| `Role core.Role` | `role` | `string(role)` / `core.Role(s)` |
| `Content string` | `content` | direct |
| `ToolCalls []core.ToolCall` | `tool_calls` | `len==0 → NULL`; else `json.Marshal` the slice; read back with `json.Unmarshal` into `[]core.ToolCall` |
| `ToolCallID string` | `tool_call_id` | `"" → NULL`; else direct (read: `NULL → ""`) |
| `CreatedAt time.Time` | `created_at` | RFC3339Nano UTC, as above |

`core.ToolCall{ID, Name, Args json.RawMessage}` serializes cleanly to/from the
`tool_calls` JSON array — `Args` is already `json.RawMessage`, so it nests without
re-encoding. The stored blob for an assistant tool turn looks like:

```json
[{"ID":"call_abc","Name":"read","Args":{"path":"go.mod"}}]
```

`seq` is a persistence-ordering detail, not a domain concept: the domain expresses
order by the position of messages in the returned `[]Message` slice, which the
adapter produces via `ORDER BY seq`. Core stays free of the ordering mechanism.

---

## 8. PRAGMAs and connection setup

For a single-user, single-process, embedded DB the pragmas are set **on every
connection in the pool** via the modernc DSN `_pragma` parameters, so no per-query
setup and no forgotten connection can violate them:

```
file:pythia.db?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)
```

| PRAGMA | Value | Why |
|---|---|---|
| `journal_mode` | `WAL` | Write-ahead logging: readers never block the single writer; durable, faster commits for the append-per-message pattern. WAL is a persistent DB property but is re-asserted per connection harmlessly. |
| `foreign_keys` | `ON` | SQLite defaults foreign keys **off**; must be enabled per connection or the `messages.session_id` FK is not enforced. |
| `busy_timeout` | `5000` (ms) | On the rare lock contention, wait rather than immediately erroring `SQLITE_BUSY`. |

Applied in the adapter's constructor (`internal/adapter/store/sqlite`) when it opens
the `*sql.DB`:

```go
const dsn = "file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"

db, err := sql.Open("sqlite", fmt.Sprintf(dsn, path)) // driver name "sqlite" (modernc.org/sqlite)
if err != nil { /* ... */ }
db.SetMaxOpenConns(1) // single-writer: serializes appends, makes seq assignment race-free, sidesteps SQLITE_BUSY
```

`SetMaxOpenConns(1)` is a deliberate simplicity choice for this single-user tool: it
serializes all writes so the `MAX(seq)+1` assignment (§5) is trivially race-free and
lock contention effectively disappears. It costs nothing here — there is exactly one
turn loop at a time per the architecture's concurrency model. WAL + `busy_timeout`
remain as defense if that ever changes.

Startup order (matches the < 200 ms cold-start NFR): open the DB, run migrations
(§9), then render the TUI. The Ollama connection stays lazy — no network at startup.

---

## 9. Migration approach

**Chosen (lightest CGO-free option):** `embed.FS` of numbered `.sql` files + a
~30-line migrator using SQLite's built-in `PRAGMA user_version` as the version
counter. No third-party migration dependency, no extra `schema_migrations` table,
nothing that pulls in CGO — consistent with the single-binary discipline.

`golang-migrate` and `goose` (library mode) are both pure-Go and would work, but for
one migration behind one port they are more machinery than the job needs (YAGNI).
`user_version` is a native SQLite counter meant for exactly this; the migrator is
small enough to unit-test directly.

Mechanism:

1. Migration files are embedded: `//go:embed migrations/*.sql` in the sqlite adapter.
2. On startup the migrator reads `PRAGMA user_version` (0 on a fresh DB).
3. For each embedded migration whose ordinal `> user_version`, in ascending order, it
   runs the file's SQL inside a transaction and, in that same transaction, sets
   `PRAGMA user_version = <ordinal>`. A failure rolls the whole step back — the DB is
   never left half-migrated.
4. Migrations are forward-only; a correction is a new higher-numbered file (per the
   skill). The initial migration is idempotent-friendly but the `user_version` gate is
   the real guard against re-running.

Sketch:

```go
//go:embed migrations/*.sql
var migrationsFS embed.FS

func migrate(db *sql.DB) error {
    var version int
    if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
        return err
    }
    files, _ := fs.Glob(migrationsFS, "migrations/*.sql") // sorted: 0001_, 0002_, ...
    sort.Strings(files)
    for i, f := range files {
        ordinal := i + 1
        if ordinal <= version {
            continue
        }
        stmt, _ := migrationsFS.ReadFile(f)
        tx, err := db.Begin()
        if err != nil { return err }
        if _, err := tx.Exec(string(stmt)); err != nil {
            tx.Rollback(); return fmt.Errorf("migration %s: %w", f, err)
        }
        // user_version takes a literal, not a bind param
        if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", ordinal)); err != nil {
            tx.Rollback(); return err
        }
        if err := tx.Commit(); err != nil { return err }
    }
    return nil
}
```

(The `user_version` PRAGMA is the one place a literal is interpolated — it takes an
integer ordinal the adapter controls, never user/model content, so SR-6's
"parameterized for all data" is upheld; every data query is parameterized.)

### `migrations/0001_init.sql`

```sql
-- 0001_init.sql
-- Pythia first slice: two aggregates — Session (root) and its ordered Message history.
-- Single messages table with typed columns + JSON tool_calls (see docs/data/first-slice-schema.md §3).

CREATE TABLE sessions (
    id         TEXT PRIMARY KEY NOT NULL,
    title      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE messages (
    id           TEXT PRIMARY KEY NOT NULL,
    session_id   TEXT NOT NULL,
    seq          INTEGER NOT NULL,
    role         TEXT NOT NULL
                 CHECK (role IN ('system','user','assistant','tool')),
    content      TEXT NOT NULL DEFAULT '',
    tool_calls   TEXT
                 CHECK (tool_calls IS NULL OR json_valid(tool_calls)),
    tool_call_id TEXT,
    created_at   TEXT NOT NULL,
    FOREIGN KEY (session_id) REFERENCES sessions(id),
    UNIQUE (session_id, seq)
) STRICT;
```

No `CREATE INDEX` statements: the PK and `UNIQUE (session_id, seq)` constraints
already create the only two indexes the slice's queries need (§6).

---

## 10. Testing note (per `principles-tdd`)

The adapter is integration-tested against a **real** modernc SQLite engine (a temp
file DB, or `:memory:` with `_pragma=foreign_keys(ON)`), never a mock — schema-level
correctness (FK enforcement, `role`/`json_valid` CHECKs, `UNIQUE(session_id, seq)`
ordering, seq monotonicity across appends) is exactly what must be exercised against
the store. The migrator is unit-tested for the fresh-DB (version 0 → 1) and
already-migrated (no-op) paths. The port contract test (incl.
`core.ErrSessionNotFound` from `GetSession`) runs against this adapter and any fake.
```