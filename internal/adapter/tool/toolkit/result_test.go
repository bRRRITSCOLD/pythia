package toolkit

import (
	"encoding/json"
	"testing"
)

func TestErr_ProducesErrorEnvelope(t *testing.T) {
	got := Err("failed to read %s", "file.txt")

	var decoded struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("Err did not produce valid JSON: %v", err)
	}
	if decoded.Error != "failed to read file.txt" {
		t.Fatalf("want formatted error message, got %q", decoded.Error)
	}
}

func TestOK_ProducesOKEnvelope(t *testing.T) {
	got := OK(map[string]string{"content": "hello"})

	var decoded struct {
		OK map[string]string `json:"ok"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("OK did not produce valid JSON: %v", err)
	}
	if decoded.OK["content"] != "hello" {
		t.Fatalf("want ok.content=hello, got %+v", decoded.OK)
	}
}
