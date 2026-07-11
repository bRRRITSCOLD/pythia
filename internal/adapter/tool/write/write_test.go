package write_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bRRRITSCOLD/pythia/internal/adapter/tool/write"
)

func TestWrite_NewFileInWorkspace_PersistsContent(t *testing.T) {
	root := t.TempDir()
	tool := write.New(root)

	args, _ := json.Marshal(map[string]string{"path": "notes.txt", "content": "hello world"})
	out, err := tool.Invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var envelope struct {
		OK struct {
			Bytes int `json:"bytes"`
		} `json:"ok"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatalf("failed to unmarshal result %q: %v", out, err)
	}
	if envelope.OK.Bytes != len("hello world") {
		t.Fatalf("want bytes=%d, got %d", len("hello world"), envelope.OK.Bytes)
	}

	got, err := os.ReadFile(filepath.Join(root, "notes.txt"))
	if err != nil {
		t.Fatalf("expected file to exist: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("want content %q, got %q", "hello world", got)
	}
}

func TestWrite_PathEscape_RejectedNoWriteOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	tool := write.New(root)

	args, _ := json.Marshal(map[string]string{"path": "../../etc/pwned.txt", "content": "malicious"})
	out, err := tool.Invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var envelope struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatalf("failed to unmarshal result %q: %v", out, err)
	}
	if envelope.Error == "" {
		t.Fatalf("want error envelope, got %q", out)
	}

	if _, statErr := os.Stat(filepath.Join(outside, "pwned.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("expected no file to exist outside root, stat err: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(root), "etc", "pwned.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("expected no file created outside root, stat err: %v", statErr)
	}
}

func TestWrite_CreatesParentDirsInsideRoot(t *testing.T) {
	root := t.TempDir()
	tool := write.New(root)

	args, _ := json.Marshal(map[string]string{"path": "a/b/c/file.txt", "content": "nested"})
	out, err := tool.Invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var envelope struct {
		OK struct {
			Bytes int `json:"bytes"`
		} `json:"ok"`
	}
	if unmarshalErr := json.Unmarshal(out, &envelope); unmarshalErr != nil {
		t.Fatalf("failed to unmarshal result %q: %v", out, unmarshalErr)
	}
	if envelope.OK.Bytes != len("nested") {
		t.Fatalf("want bytes=%d, got %d", len("nested"), envelope.OK.Bytes)
	}

	got, err := os.ReadFile(filepath.Join(root, "a", "b", "c", "file.txt"))
	if err != nil {
		t.Fatalf("expected nested file to exist: %v", err)
	}
	if string(got) != "nested" {
		t.Fatalf("want content %q, got %q", "nested", got)
	}
}

func TestWrite_MissingContentArg_ReturnsErrorEnvelope(t *testing.T) {
	root := t.TempDir()
	tool := write.New(root)

	args, _ := json.Marshal(map[string]string{"path": "notes.txt"})
	out, err := tool.Invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var envelope struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatalf("failed to unmarshal result %q: %v", out, err)
	}
	if envelope.Error == "" {
		t.Fatalf("want error envelope, got %q", out)
	}

	if _, statErr := os.Stat(filepath.Join(root, "notes.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("expected no file to be created, stat err: %v", statErr)
	}
}

func TestWrite_Schema_AdvertisesPathAndContent(t *testing.T) {
	tool := write.New(t.TempDir())

	schema := tool.Schema()
	if schema.Name != "write" {
		t.Fatalf("want schema name %q, got %q", "write", schema.Name)
	}
	if schema.Description == "" {
		t.Fatalf("want non-empty description")
	}

	params := string(schema.Parameters)
	if !strings.Contains(params, `"path"`) {
		t.Fatalf("want schema params to advertise %q, got %s", "path", params)
	}
	if !strings.Contains(params, `"content"`) {
		t.Fatalf("want schema params to advertise %q, got %s", "content", params)
	}
}
