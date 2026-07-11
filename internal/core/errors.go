package core

import "errors"

// Sentinel errors shared across core ports (docs/architecture/first-slice.md
// §2.5). Callers compare against these with errors.Is.
var (
	// ErrSessionNotFound is returned by SessionRepository.GetSession when the
	// requested session id does not exist.
	ErrSessionNotFound = errors.New("session not found")

	// ErrMaxIterations is returned when the Agent's tool-call loop exceeds
	// its configured bound (WithMaxIterations) without reaching a turn that
	// returns no further tool calls.
	ErrMaxIterations = errors.New("max tool-call iterations exceeded")
)
