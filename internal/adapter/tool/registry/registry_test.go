package registry_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bRRRITSCOLD/pythia/internal/adapter/tool/registry"
	"github.com/bRRRITSCOLD/pythia/internal/core"
)

// stubTool is a minimal core.Tool fake for exercising the registry without
// pulling in any real tool package (registry must not import them).
type stubTool struct {
	name string
}

func (s stubTool) Schema() core.ToolSchema {
	return core.ToolSchema{Name: s.name, Description: "stub", Parameters: json.RawMessage(`{}`)}
}

func (s stubTool) Invoke(context.Context, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

func TestRegistry_Get_RegisteredTool_ReturnsIt(t *testing.T) {
	tl := stubTool{name: "read"}
	r, err := registry.New(tl)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	got, ok := r.Get("read")
	if !ok {
		t.Fatalf("Get(%q) ok=false, want true", "read")
	}
	if got.Schema().Name != "read" {
		t.Fatalf("Get(%q) returned tool named %q", "read", got.Schema().Name)
	}
}

func TestRegistry_Get_UnknownTool_ReturnsFalse(t *testing.T) {
	r, err := registry.New()
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatal("Get(\"nope\") ok=true, want false for unregistered tool")
	}
}

func TestRegistry_Schemas_ReturnsAllRegisteredSchemas(t *testing.T) {
	r, err := registry.New(stubTool{name: "read"}, stubTool{name: "write"})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	schemas := r.Schemas()
	if len(schemas) != 2 {
		t.Fatalf("Schemas() returned %d schemas, want 2", len(schemas))
	}
	names := map[string]bool{}
	for _, s := range schemas {
		names[s.Name] = true
	}
	if !names["read"] || !names["write"] {
		t.Fatalf("Schemas() = %+v, want names read and write", schemas)
	}
}

func TestRegistry_New_DuplicateNames_ReturnsError(t *testing.T) {
	if _, err := registry.New(stubTool{name: "read"}, stubTool{name: "read"}); err == nil {
		t.Fatal("New with duplicate tool names returned nil error, want error")
	}
}

// TestRegistry_ImplementsCoreToolRegistry pins *registry.Registry as a
// core.ToolRegistry at compile time.
func TestRegistry_ImplementsCoreToolRegistry(t *testing.T) {
	var _ core.ToolRegistry = (*registry.Registry)(nil)
}
