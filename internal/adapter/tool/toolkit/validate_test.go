package toolkit

import (
	"encoding/json"
	"testing"
)

type sample struct {
	Path string `json:"path" validate:"required"`
}

func TestValidate_MissingRequiredField_ReturnsError(t *testing.T) {
	var s sample

	if err := Validate(json.RawMessage(`{}`), &s); err == nil {
		t.Fatal("want error for missing path")
	}
}

func TestValidate_UnknownField_Rejected(t *testing.T) {
	var s sample

	if err := Validate(json.RawMessage(`{"path":"x","evil":1}`), &s); err == nil {
		t.Fatal("want error for unknown field")
	}
}

func TestValidate_MalformedJSON_ReturnsErrorNoPanic(t *testing.T) {
	var s sample

	if err := Validate(json.RawMessage(`{not json`), &s); err == nil {
		t.Fatal("want error for malformed json")
	}
}

func TestValidate_ValidArgs_PopulatesStruct(t *testing.T) {
	var s sample

	err := Validate(json.RawMessage(`{"path":"go.mod"}`), &s)

	if err != nil || s.Path != "go.mod" {
		t.Fatalf("err=%v s=%+v", err, s)
	}
}
