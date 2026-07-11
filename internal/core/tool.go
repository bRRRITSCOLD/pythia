package core

import (
	"context"
	"encoding/json"
)

// Tool is one capability the model may invoke. A built-in in-process tool and a
// future out-of-process go-plugin gRPC proxy implement this SAME interface, so
// core is agnostic to in-process vs. out-of-process execution. This is the
// seam; no go-plugin dependency exists in this slice.
//
// Contract:
//   - Schema is stable and advertised to the Provider.
//   - Invoke executes with JSON args and returns a JSON result.
//   - ctx carries the deadline/cancellation (e.g. the bash timeout).
//   - A returned error is an infrastructure/execution failure. A tool that
//     "fails in a way the model should see and react to" (bad path, non-zero
//     exit) returns that in the JSON result with a nil error, so the loop feeds
//     it back to the model rather than aborting the turn.
type Tool interface {
	Schema() ToolSchema
	Invoke(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}

// ToolRegistry holds the available tools and exposes their schemas to the
// Provider. The first-slice impl is an in-process map. A future impl can merge
// gRPC-plugin tools behind this same interface without touching core.
type ToolRegistry interface {
	Schemas() []ToolSchema        // all advertised schemas, for the Provider
	Get(name string) (Tool, bool) // resolve by name; ok=false if unregistered
}
