package read

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRead_ExistingFileInWorkspace_ReturnsContent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	tool := New(root, 1024)

	out, err := tool.Invoke(context.Background(), json.RawMessage(`{"path":"hello.txt"}`))
	if err != nil {
		t.Fatalf("want nil Go error, got %v", err)
	}

	var envelope struct {
		OK struct {
			Content   string `json:"content"`
			Truncated bool   `json:"truncated"`
		} `json:"ok"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if envelope.OK.Content != "hello world" {
		t.Errorf("content = %q, want %q", envelope.OK.Content, "hello world")
	}
	if envelope.OK.Truncated {
		t.Errorf("truncated = true, want false")
	}
}

func TestRead_PathEscape_ReturnsErrorEnvelopeNilGoError(t *testing.T) {
	root := t.TempDir()

	tool := New(root, 1024)

	out, err := tool.Invoke(context.Background(), json.RawMessage(`{"path":"../escape.txt"}`))
	if err != nil {
		t.Fatalf("want nil Go error, got %v", err)
	}

	assertErrorEnvelope(t, out)
}

func TestRead_FileLargerThanCap_TruncatesAndFlags(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("a", 20)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(content), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	tool := New(root, 10)

	out, err := tool.Invoke(context.Background(), json.RawMessage(`{"path":"big.txt"}`))
	if err != nil {
		t.Fatalf("want nil Go error, got %v", err)
	}

	var envelope struct {
		OK struct {
			Content   string `json:"content"`
			Truncated bool   `json:"truncated"`
		} `json:"ok"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !envelope.OK.Truncated {
		t.Errorf("truncated = false, want true")
	}
	if len(envelope.OK.Content) != 10 {
		t.Errorf("content len = %d, want 10", len(envelope.OK.Content))
	}
	if envelope.OK.Content != strings.Repeat("a", 10) {
		t.Errorf("content = %q, want first 10 bytes", envelope.OK.Content)
	}
}

func TestRead_MissingFile_ReturnsErrorEnvelope(t *testing.T) {
	root := t.TempDir()

	tool := New(root, 1024)

	out, err := tool.Invoke(context.Background(), json.RawMessage(`{"path":"nope.txt"}`))
	if err != nil {
		t.Fatalf("want nil Go error, got %v", err)
	}

	assertErrorEnvelope(t, out)
}

func TestRead_MalformedArgs_ReturnsErrorEnvelope(t *testing.T) {
	root := t.TempDir()

	tool := New(root, 1024)

	out, err := tool.Invoke(context.Background(), json.RawMessage(`{not json`))
	if err != nil {
		t.Fatalf("want nil Go error, got %v", err)
	}

	assertErrorEnvelope(t, out)
}

func TestRead_Schema_AdvertisesPathParam(t *testing.T) {
	tool := New(t.TempDir(), 1024)

	schema := tool.Schema()
	if schema.Name != "read" {
		t.Errorf("name = %q, want %q", schema.Name, "read")
	}

	var params struct {
		Properties struct {
			Path struct {
				Type string `json:"type"`
			} `json:"path"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema.Parameters, &params); err != nil {
		t.Fatalf("unmarshal schema parameters: %v", err)
	}
	if params.Properties.Path.Type != "string" {
		t.Errorf("path type = %q, want %q", params.Properties.Path.Type, "string")
	}
	found := false
	for _, r := range params.Required {
		if r == "path" {
			found = true
		}
	}
	if !found {
		t.Errorf("required = %v, want to include %q", params.Required, "path")
	}
}

func assertErrorEnvelope(t *testing.T, out json.RawMessage) {
	t.Helper()
	var envelope struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if envelope.Error == "" {
		t.Errorf("error = %q, want non-empty", envelope.Error)
	}
}
