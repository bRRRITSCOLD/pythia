# 0004 — Module / package layout and the inward dependency rule

**Status:** Accepted

## Context

Pythia is ports-and-adapters by mandate (stack profile: "Architecture is
invariant"). The load-bearing structural guarantee is that the **agent core —
the domain model plus the turn loop plus the ports — must not import any
adapter** (Ollama, SQLite, Bubble Tea, tools). If that rule holds, swapping a
Provider, a store, or the UI, and adding tools, are all additive changes that
never touch core (acceptance criterion). If it leaks, the seams from ADRs
0001–0003 are worthless.

This requires a concrete package layout and a rule about which way imports
point, plus a way to keep it true as the code grows. Layout options:

| Option | Strengths | Weaknesses | When to prefer |
|--------|-----------|------------|----------------|
| **Flat `package main`** | Fastest to start | No enforced boundaries; core and adapters intermix; the whole thesis collapses | Throwaway script |
| **Layer-named packages** (`models`, `services`, `handlers`) | Familiar | Layers by technical role, not by dependency direction; tends to grow circular deps | Simple CRUD apps |
| **`core` + `adapter/*` + `cmd` with an inward dependency rule** (chosen) | Encodes ports-and-adapters directly; dependency direction is explicit and testable; composition isolated to `cmd` | One more layer of directory discipline | A framework whose seams are the product — our case |

## Decision

Module path `github.com/bRRRITSCOLD/pythia`. Package topology:

```
cmd/pythia/            composition root / DI wiring — the ONLY place adapters meet
internal/config/       env → validated Config
internal/core/         domain types + ports + agent turn loop (std-lib only)
internal/adapter/
    provider/ollama/   Provider impl
    tool/registry/     ToolRegistry impl
    tool/{read,write,edit,bash}/  Tool impls
    store/sqlite/      SessionRepository impl + migrations
    tui/               Bubble Tea program (depends on core, not on Provider)
```

**Dependency rule (invariant):** dependencies point inward.
- `internal/core` imports **only the standard library** — no adapter, no
  third-party runtime lib.
- `internal/adapter/*` imports `internal/core` (to implement its ports and use
  its domain types) plus third-party libs.
- `cmd/pythia` imports everything and performs all DI wiring — construct each
  adapter, inject them into `core.NewAgent`, hand the Agent to the TUI, run.

The TUI depends on core's `AgentEvent` stream, **not** on `Provider`, so even the
UI is decoupled from the model transport.

This rule is enforced by a **fitness function**: a dependency-direction test (or
`depguard`/`go list` check in CI) that fails the build if `internal/core` gains
a forbidden import.

## Consequences

- **Easier:** swapping Provider/store/UI and adding tools are additive adapter
  changes; core stays put — the acceptance criterion is structurally guaranteed,
  not merely intended.
- **Easier:** core is unit-testable with fakes and needs no network, DB, or TTY.
- **Easier:** the single composition root makes the whole wiring graph readable
  in one file and keeps `main` the only cgo-adjacent surface.
- **Harder:** contributors must respect the direction — a convenient "just import
  the adapter from core" shortcut is forbidden. The CI fitness function makes the
  violation loud instead of silent.
- **Obligation:** anything shared across adapters must live in core (as a port or
  domain type) or in its own leaf package, never be reached for sideways between
  adapters.
