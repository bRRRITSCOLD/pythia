package toolkit

import (
	"encoding/json"
	"fmt"
)

// errorEnvelope and okEnvelope are the frozen tool-result envelope shapes
// (locked cross-cutting decision #3) every built-in tool returns.
type errorEnvelope struct {
	Error string `json:"error"`
}

type okEnvelope struct {
	OK any `json:"ok"`
}

// Err formats a message with fmt.Sprintf and marshals it into the frozen
// error envelope: {"error":"..."}. Marshaling a string can never fail, so
// the json.Marshal error is intentionally discarded.
func Err(format string, a ...any) json.RawMessage {
	out, _ := json.Marshal(errorEnvelope{Error: fmt.Sprintf(format, a...)})
	return out
}

// OK marshals v into the frozen soft-result envelope: {"ok":<v>}. Used at
// call sites that return a nil Go error alongside a tool-level result.
func OK(v any) json.RawMessage {
	out, err := json.Marshal(okEnvelope{OK: v})
	if err != nil {
		return Err("toolkit: marshal ok result: %v", err)
	}
	return out
}
