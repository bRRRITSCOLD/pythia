// Package edit implements the "edit" built-in tool: a workspace-scoped,
// unique-substring replace within an existing file.
package edit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/bRRRITSCOLD/pythia/internal/adapter/tool/toolkit"
	"github.com/bRRRITSCOLD/pythia/internal/core"
)

// schemaParameters is the JSON Schema advertised to the Provider for the
// "edit" tool's args: path, old, new — all required strings.
var schemaParameters = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "workspace-relative file path"},
		"old": {"type": "string", "description": "exact substring to replace; must occur exactly once"},
		"new": {"type": "string", "description": "replacement text"}
	},
	"required": ["path", "old", "new"],
	"additionalProperties": false
}`)

// args is the validated shape of the "edit" tool's arguments.
type args struct {
	Path string `json:"path" validate:"required"`
	Old  string `json:"old" validate:"required"`
	New  string `json:"new" validate:"required"`
}

// tool implements core.Tool for workspace-scoped file editing.
type tool struct {
	workspaceRoot string
}

// New returns the "edit" core.Tool, scoped to workspaceRoot (SR-2).
func New(workspaceRoot string) core.Tool {
	return &tool{workspaceRoot: workspaceRoot}
}

func (t *tool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name:        "edit",
		Description: "Replace an exact, uniquely-occurring substring in a workspace file.",
		Parameters:  schemaParameters,
	}
}

// Invoke validates args, resolves path within the workspace, and replaces
// the single occurrence of old with new. Soft failures (bad args, path
// escape, missing file, old not found, old not unique) are returned as a
// {"error":...} envelope with a nil Go error, leaving the file unchanged.
func (t *tool) Invoke(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var a args
	if err := toolkit.Validate(raw, &a); err != nil {
		return toolkit.Err("edit: invalid args: %v", err), nil
	}

	resolved, err := toolkit.ResolvePath(t.workspaceRoot, a.Path)
	if err != nil {
		if errors.Is(err, toolkit.ErrPathEscape) {
			return toolkit.Err("edit: %v", err), nil
		}
		return toolkit.Err("edit: resolve path: %v", err), nil
	}

	content, err := os.ReadFile(resolved)
	if err != nil {
		return toolkit.Err("edit: read file: %v", err), nil
	}

	count := strings.Count(string(content), a.Old)
	switch {
	case count == 0:
		return toolkit.Err("edit: old string not found in %s", a.Path), nil
	case count > 1:
		return toolkit.Err("edit: old string is not unique in %s (%d occurrences)", a.Path, count), nil
	}

	updated := strings.Replace(string(content), a.Old, a.New, 1)
	if err := os.WriteFile(resolved, []byte(updated), 0o644); err != nil {
		return toolkit.Err("edit: write file: %v", err), nil
	}

	return toolkit.OK(map[string]int{"replaced": 1}), nil
}
