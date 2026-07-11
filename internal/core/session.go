package core

import "context"

// SessionRepository persists sessions and their message history. Core depends
// on this port only; the SQLite adapter lives behind it and core never sees SQL.
//
// Contract:
//   - GetSession returns ErrSessionNotFound when absent.
//   - AppendMessage appends one message; ordering is by CreatedAt.
//   - Messages returns the full ordered history for a session (used to resume
//     across restarts and to build each ChatRequest).
type SessionRepository interface {
	CreateSession(ctx context.Context, s Session) error
	GetSession(ctx context.Context, id string) (Session, error)
	AppendMessage(ctx context.Context, m Message) error
	Messages(ctx context.Context, sessionID string) ([]Message, error)
}
