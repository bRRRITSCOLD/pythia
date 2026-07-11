# 0002 — Tool / ToolRegistry seam admitting future out-of-process gRPC plugins

**Status:** Accepted

## Context

The agent core must dispatch exactly 4 built-in tools (read, write, bash, edit)
this slice. But Pythia's larger thesis (stack profile) is that capabilities
extend via **out-of-process `hashicorp/go-plugin` gRPC** tools later. The core
turn loop must be **agnostic to whether a tool runs in-process or out-of-process
behind gRPC** — adding a plugin tool later must not touch core (spec resolved
decision 3; acceptance criterion: adding a tool requires no core change).

The tension: build the plugin system now (premature — YAGNI, adds gRPC/go-plugin
weight and a process-lifecycle problem to a thin slice) versus hard-code the 4
tools (cheap now, but bakes in an in-process assumption that a future gRPC proxy
can't satisfy without refactoring core).

Options considered:

| Option | Strengths | Weaknesses | When to prefer |
|--------|-----------|------------|----------------|
| **A. Hard-code 4 tools in core** | Least code now | Core knows each tool concretely; a gRPC tool can't be added without changing core; violates the acceptance criterion | Throwaway prototype |
| **B. `Tool` interface + `ToolRegistry`, in-process map impl now** (chosen) | Core depends only on the interface; built-ins and a future gRPC proxy implement the identical `Tool`; no go-plugin dependency yet | One interface method more than hard-coding | A slice that must preserve an extension seam — our case |
| **C. Build go-plugin gRPC now** | Real isolation immediately | Large YAGNI: gRPC, subprocess lifecycle, versioning — none needed to prove the slice | When a real third-party plugin exists |

The key insight: an out-of-process gRPC tool proxy is *just another `Tool`
implementation* whose `Invoke` marshals args over gRPC. If `Tool` is defined as
`Schema()` + `Invoke(ctx, argsJSON) (resultJSON, error)`, the proxy fits the
same interface with no core awareness of the transport.

## Decision

Define in `internal/core`:

```go
type Tool interface {
	Schema() ToolSchema
	Invoke(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}

type ToolRegistry interface {
	Schemas() []ToolSchema
	Get(name string) (Tool, bool)
}
```

The 4 built-ins are `Tool` impls in `internal/adapter/tool/{read,write,edit,bash}`.
An in-process map-backed `ToolRegistry` lives in `internal/adapter/tool/registry`.
The registry exposes `Schemas()` to the Provider and resolves `Get(name)` for
the turn loop. **No `hashicorp/go-plugin` dependency exists in this slice** — only
the seam. JSON in / JSON out is the ABI, chosen precisely because it survives an
in-process → gRPC transport change unchanged.

## Consequences

- **Easier:** a future gRPC-plugin tool is a new `Tool` impl (a proxy) plus a
  registry that merges plugin schemas — core is untouched, satisfying the
  acceptance criterion.
- **Easier:** each tool self-describes via `Schema()`, so the Provider advertises
  tools uniformly and tool-arg validation lives at the adapter boundary (SR-5).
- **Harder:** the JSON-RawMessage ABI means richer typed contracts aren't
  compiler-checked across the boundary; each tool must validate its own args.
  This is the deliberate cost of transport-agnosticism.
- **Obligation:** the registry later grows plugin lifecycle management
  (start/stop/health of gRPC subprocesses) behind the same `ToolRegistry`
  interface — an additive change, not a breaking one.
