// Package read implements the "read" built-in tool: read a file inside the
// workspace, contained by SR-2 and capped by SR-4b.
package read

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/bRRRITSCOLD/pythia/internal/adapter/tool/toolkit"
	"github.com/bRRRITSCOLD/pythia/internal/core"
)

// schemaParameters is the frozen JSON Schema advertised for this tool: a
// single required "path" string.
const schemaParameters = `{
	"type": "object",
	"properties": {
		"path": {
			"type": "string",
			"description": "Path to the file, relative to the workspace root."
		}
	},
	"required": ["path"],
	"additionalProperties": false
}`

// args is the decoded, validated shape of the tool's JSON arguments.
type args struct {
	Path string `json:"path" validate:"required"`
}

// result is the frozen "ok" payload shape for a successful read.
type result struct {
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

// readTool implements core.Tool. workspaceRoot bounds every path (SR-2);
// maxBytes bounds how much of a file is returned (SR-4b).
type readTool struct {
	workspaceRoot string
	maxBytes      int64
}

// New returns the "read" tool, contained to workspaceRoot (SR-2) and capped
// at maxBytes per read (SR-4b).
func New(workspaceRoot string, maxBytes int64) core.Tool {
	return &readTool{workspaceRoot: workspaceRoot, maxBytes: maxBytes}
}

// Schema advertises the tool's name, description, and parameter shape to
// the Provider.
func (t *readTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name:        "read",
		Description: "Read a file inside the workspace and return its content.",
		Parameters:  json.RawMessage(schemaParameters),
	}
}

// Invoke reads the file named by args.Path, bounded to workspaceRoot (SR-2)
// and truncated at maxBytes (SR-4b). Malformed args, a path escape, and any
// I/O failure (including a missing file) all return the frozen error
// envelope with a nil Go error, so the turn loop feeds the failure back to
// the model rather than aborting.
func (t *readTool) Invoke(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var a args
	if err := toolkit.Validate(raw, &a); err != nil {
		return toolkit.Err("read: invalid args: %v", err), nil
	}

	resolved, err := toolkit.ResolvePath(t.workspaceRoot, a.Path)
	if err != nil {
		if errors.Is(err, toolkit.ErrPathEscape) {
			return toolkit.Err("read: %v", err), nil
		}
		return toolkit.Err("read: resolve path: %v", err), nil
	}

	f, err := os.Open(resolved)
	if err != nil {
		return toolkit.Err("read: %v", err), nil
	}
	defer f.Close()

	// Read one byte beyond the cap so a same-size-as-cap file is
	// distinguishable from an over-cap file (SR-4b truncation detection).
	buf := make([]byte, t.maxBytes+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return toolkit.Err("read: %v", err), nil
	}

	truncated := int64(n) > t.maxBytes
	if truncated {
		n = int(t.maxBytes)
	}

	return toolkit.OK(result{Content: string(buf[:n]), Truncated: truncated}), nil
}
