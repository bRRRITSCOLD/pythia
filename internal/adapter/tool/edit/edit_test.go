package edit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var ctx = context.Background()

func TestEdit_UniqueOldString_ReplacesAndPersists(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("foo bar"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := New(root).Invoke(ctx, json.RawMessage(`{"path":"f","old":"bar","new":"baz"}`))

	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	b, readErr := os.ReadFile(filepath.Join(root, "f"))
	if readErr != nil {
		t.Fatalf("read back: %v", readErr)
	}
	if string(b) != "foo baz" {
		t.Fatalf("file=%q res=%s", b, res)
	}
	if !strings.Contains(string(res), `"replaced":1`) {
		t.Fatalf("want replaced:1 in result, got %s", res)
	}
}

func TestEdit_OldStringNotFound_ReturnsErrorEnvelopeNoWrite(t *testing.T) {
	root := t.TempDir()
	original := []byte("foo bar")
	if err := os.WriteFile(filepath.Join(root, "f"), original, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := New(root).Invoke(ctx, json.RawMessage(`{"path":"f","old":"nope","new":"baz"}`))

	if err != nil {
		t.Fatalf("want soft error, got Go error: %v", err)
	}
	if !strings.Contains(string(res), "error") {
		t.Fatalf("want error envelope, got %s", res)
	}
	b, _ := os.ReadFile(filepath.Join(root, "f"))
	if string(b) != string(original) {
		t.Fatalf("file was modified: %q", b)
	}
}

func TestEdit_OldStringNotUnique_ReturnsErrorEnvelope(t *testing.T) {
	root := t.TempDir()
	original := []byte("aaaa")
	if err := os.WriteFile(filepath.Join(root, "f"), original, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := New(root).Invoke(ctx, json.RawMessage(`{"path":"f","old":"aa","new":"b"}`))

	if err != nil {
		t.Fatalf("want soft error, got Go error: %v", err)
	}
	if !strings.Contains(string(res), "error") {
		t.Fatalf("want error envelope, got %s", res)
	}
	b, _ := os.ReadFile(filepath.Join(root, "f"))
	if string(b) != string(original) {
		t.Fatalf("file was modified: %q", b)
	}
}

func TestEdit_PathEscape_Rejected(t *testing.T) {
	root := t.TempDir()

	res, err := New(root).Invoke(ctx, json.RawMessage(`{"path":"../../etc/passwd","old":"a","new":"b"}`))

	if err != nil {
		t.Fatalf("want soft error, got Go error: %v", err)
	}
	if !strings.Contains(string(res), "error") {
		t.Fatalf("want error envelope, got %s", res)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(root), "etc", "passwd")); statErr == nil {
		t.Fatal("wrote outside workspace — SR-2 breach")
	}
}

func TestEdit_MissingFile_ReturnsErrorEnvelope(t *testing.T) {
	root := t.TempDir()

	res, err := New(root).Invoke(ctx, json.RawMessage(`{"path":"nope.txt","old":"a","new":"b"}`))

	if err != nil {
		t.Fatalf("want soft error, got Go error: %v", err)
	}
	if !strings.Contains(string(res), "error") {
		t.Fatalf("want error envelope, got %s", res)
	}
}

func TestEdit_MalformedArgs_ReturnsErrorEnvelope(t *testing.T) {
	root := t.TempDir()

	res, err := New(root).Invoke(ctx, json.RawMessage(`{"path":123}`))

	if err != nil {
		t.Fatalf("want soft error, got Go error: %v", err)
	}
	if !strings.Contains(string(res), "error") {
		t.Fatalf("want error envelope, got %s", res)
	}
}

func TestEdit_Schema_AdvertisesPathOldNew(t *testing.T) {
	schema := New(t.TempDir()).Schema()

	if schema.Name != "edit" {
		t.Fatalf("want name=edit, got %q", schema.Name)
	}
	params := string(schema.Parameters)
	for _, field := range []string{"path", "old", "new"} {
		if !strings.Contains(params, field) {
			t.Fatalf("schema params missing %q: %s", field, params)
		}
	}
}
