package core

import (
	"crypto/rand"
	"encoding/hex"
)

// NewID returns a fresh, hex-encoded 128-bit random identifier suitable for
// Message.ID, Session.ID, and ToolCall.ID. It uses only crypto/rand and
// encoding/hex (stdlib) so core stays free of any third-party UUID
// dependency (docs/adr/0004).
//
// A read failure from crypto/rand indicates the OS entropy source is broken,
// which is unrecoverable for this process; NewID panics rather than silently
// returning a low-entropy or empty ID.
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("core: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}
