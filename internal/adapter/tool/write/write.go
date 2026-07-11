// Package write implements the built-in "write" tool: it creates or
// overwrites a file inside the workspace with the given content.
package write

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/bRRRITSCOLD/pythia/internal/adapter/tool/toolkit"
	"github.com/bRRRITSCOLD/pythia/internal/core"
)

// schemaParameters is the JSON Schema advertised to the Provider, describing
// the args object write.Invoke accepts.
const schemaParameters = `{
	"type": "object",
	"properties": {
		"path": {
			"type": "string",
			"description": "Workspace-relative path of the file to write."
		},
		"content": {
			"type": "string",
			"description": "The full content to write to the file."
		}
	},
	"required": ["path", "content"],
	"additionalProperties": false
}`

// writeArgs is the decoded shape of the tool's JSON args.
type writeArgs struct {
	Path    string `json:"path" validate:"required"`
	Content string `json:"content" validate:"required"`
}

// writeResult is the payload marshaled inside the {"ok": ...} envelope on
// success.
type writeResult struct {
	Bytes int `json:"bytes"`
}

// tool is the in-process "write" core.Tool implementation. It is confined to
// workspaceRoot (SR-2): every write target is resolved through
// toolkit.ResolvePath and rejected before any filesystem mutation occurs if
// it would escape the root.
type tool struct {
	workspaceRoot string
}

// New constructs the "write" tool, confined to writes within workspaceRoot.
func New(workspaceRoot string) core.Tool {
	return &tool{workspaceRoot: workspaceRoot}
}

// Schema advertises the "write" tool's name, purpose, and parameters.
func (t *tool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name:        "write",
		Description: "Creates or overwrites a file inside the workspace with the given content, creating any missing parent directories.",
		Parameters:  json.RawMessage(schemaParameters),
	}
}

// Invoke decodes and validates args (SR-5), resolves the target path within
// workspaceRoot (SR-2) — rejecting an escape without touching the
// filesystem — creates any missing parent directories inside the root, and
// writes content to the resolved path. A bad path or invalid args is
// reported as a soft tool-level error envelope (nil Go error) so the loop
// feeds it back to the model rather than aborting the turn.
func (t *tool) Invoke(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a writeArgs
	if err := toolkit.Validate(args, &a); err != nil {
		return toolkit.Err("write: %v", err), nil
	}

	resolved, err := toolkit.ResolvePath(t.workspaceRoot, a.Path)
	if err != nil {
		return toolkit.Err("write: %v", err), nil
	}

	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return toolkit.Err("write: create parent directories: %v", err), nil
	}

	if err := os.WriteFile(resolved, []byte(a.Content), 0o644); err != nil {
		return toolkit.Err("write: %v", err), nil
	}

	return toolkit.OK(writeResult{Bytes: len(a.Content)}), nil
}
