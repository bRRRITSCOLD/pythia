package toolkit

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/go-playground/validator/v10"
)

// validate is a package-level singleton — go-playground/validator
// recommends caching a single *validator.Validate instance since it builds
// and caches struct-reflection metadata internally.
var validate = validator.New()

// Validate decodes args into dst — rejecting unknown fields (SR-5) — then
// runs struct-tag validation over the result. It never panics: malformed
// JSON, unknown fields, and failed validation rules all return a wrapped
// error.
func Validate(args json.RawMessage, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("toolkit: decode args: %w", err)
	}

	if err := validate.Struct(dst); err != nil {
		return fmt.Errorf("toolkit: invalid args: %w", err)
	}

	return nil
}
