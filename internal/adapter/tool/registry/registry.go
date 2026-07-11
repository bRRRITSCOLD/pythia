// Package registry is the in-process, map-backed core.ToolRegistry adapter
// (spec decision 3, docs/adr/0002). It holds core.Tool values the caller
// (cmd) constructs and passes in — it never imports the concrete tool
// packages itself, so it stays free of the contention those packages'
// parallel development would otherwise create. A future gRPC-plugin
// registry can drop in behind the same core.ToolRegistry interface without
// any change to callers.
package registry

import (
	"fmt"
	"sort"

	"github.com/bRRRITSCOLD/pythia/internal/core"
)

// Registry is a fixed, in-process set of core.Tool values keyed by name. It
// implements core.ToolRegistry.
type Registry struct {
	tools map[string]core.Tool
}

// New builds a Registry from the given tools, keyed by each tool's
// Schema().Name. It returns an error if two tools share a name, so callers
// (cmd) fail fast at startup rather than silently shadowing a tool.
func New(tools ...core.Tool) (*Registry, error) {
	m := make(map[string]core.Tool, len(tools))
	for _, t := range tools {
		name := t.Schema().Name
		if _, exists := m[name]; exists {
			return nil, fmt.Errorf("registry: duplicate tool name %q", name)
		}
		m[name] = t
	}
	return &Registry{tools: m}, nil
}

// Get resolves a tool by name; ok is false when no tool is registered under
// that name.
func (r *Registry) Get(name string) (core.Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Schemas returns every registered tool's schema, ordered by name so the
// result is stable across calls despite the underlying map's random
// iteration order.
func (r *Registry) Schemas() []core.ToolSchema {
	schemas := make([]core.ToolSchema, 0, len(r.tools))
	for _, t := range r.tools {
		schemas = append(schemas, t.Schema())
	}
	sort.Slice(schemas, func(i, j int) bool { return schemas[i].Name < schemas[j].Name })
	return schemas
}
