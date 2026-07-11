package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bRRRITSCOLD/pythia/internal/core"
)

// timeLayout is RFC3339Nano UTC: inspectable, lexicographically sortable,
// and a clean time.Time round-trip (data-doc §4).
const timeLayout = time.RFC3339Nano

// CreateSession inserts one session row. All values are bound parameters
// (SR-6).
func (r *Repo) CreateSession(ctx context.Context, s core.Session) error {
	const q = `INSERT INTO sessions (id, title, created_at, updated_at) VALUES (?, ?, ?, ?)`
	_, err := r.db.ExecContext(ctx, q,
		s.ID, s.Title, formatTime(s.CreatedAt), formatTime(s.UpdatedAt))
	if err != nil {
		return fmt.Errorf("sqlite: create session %s: %w", s.ID, err)
	}
	return nil
}

// GetSession loads one session by id, mapping a missing row to
// core.ErrSessionNotFound per the port contract.
func (r *Repo) GetSession(ctx context.Context, id string) (core.Session, error) {
	const q = `SELECT id, title, created_at, updated_at FROM sessions WHERE id = ?`

	var (
		s                    core.Session
		createdAt, updatedAt string
	)
	err := r.db.QueryRowContext(ctx, q, id).Scan(&s.ID, &s.Title, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Session{}, core.ErrSessionNotFound
	}
	if err != nil {
		return core.Session{}, fmt.Errorf("sqlite: get session %s: %w", id, err)
	}

	s.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return core.Session{}, fmt.Errorf("sqlite: parse created_at for session %s: %w", id, err)
	}
	s.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return core.Session{}, fmt.Errorf("sqlite: parse updated_at for session %s: %w", id, err)
	}
	return s, nil
}

// AppendMessage inserts one message, assigning its per-session seq
// atomically inside the INSERT via a correlated subquery (data-doc §5), so
// no read round-trip is needed and — combined with the single-writer
// connection pool (data-doc §8) — the assignment is race-free. tool_calls is
// NULL when the message has none; tool_call_id is NULL when empty. Every
// value is bound (SR-6); message content is never logged (SR-7).
func (r *Repo) AppendMessage(ctx context.Context, m core.Message) error {
	id := m.ID
	if id == "" {
		id = core.NewID()
	}

	toolCalls, err := marshalToolCalls(m.ToolCalls)
	if err != nil {
		return fmt.Errorf("sqlite: marshal tool_calls for message %s: %w", id, err)
	}

	var toolCallID any
	if m.ToolCallID != "" {
		toolCallID = m.ToolCallID
	}

	const q = `
		INSERT INTO messages (id, session_id, seq, role, content, tool_calls, tool_call_id, created_at)
		VALUES (?, ?,
			(SELECT COALESCE(MAX(seq), 0) + 1 FROM messages WHERE session_id = ?),
			?, ?, ?, ?, ?)`

	_, err = r.db.ExecContext(ctx, q,
		id, m.SessionID, m.SessionID,
		string(m.Role), m.Content, toolCalls, toolCallID, formatTime(m.CreatedAt))
	if err != nil {
		return fmt.Errorf("sqlite: append message %s: %w", id, err)
	}
	return nil
}

// Messages returns the full ordered history for a session, replayed by the
// monotonic per-session seq assigned at append time (data-doc §5) — not by
// created_at, which is audit data only.
func (r *Repo) Messages(ctx context.Context, sessionID string) ([]core.Message, error) {
	const q = `
		SELECT id, session_id, role, content, tool_calls, tool_call_id, created_at
		FROM messages
		WHERE session_id = ?
		ORDER BY seq`

	rows, err := r.db.QueryContext(ctx, q, sessionID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list messages for session %s: %w", sessionID, err)
	}
	defer rows.Close()

	var out []core.Message
	for rows.Next() {
		var (
			m          core.Message
			role       string
			toolCalls  sql.NullString
			toolCallID sql.NullString
			createdAt  string
		)
		if err := rows.Scan(&m.ID, &m.SessionID, &role, &m.Content, &toolCalls, &toolCallID, &createdAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan message row for session %s: %w", sessionID, err)
		}

		m.Role = core.Role(role)
		if toolCallID.Valid {
			m.ToolCallID = toolCallID.String
		}
		m.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("sqlite: parse created_at for message %s: %w", m.ID, err)
		}
		m.ToolCalls, err = unmarshalToolCalls(toolCalls)
		if err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal tool_calls for message %s: %w", m.ID, err)
		}

		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate messages for session %s: %w", sessionID, err)
	}
	return out, nil
}

// marshalToolCalls encodes tool calls to a JSON array, or nil (SQL NULL)
// when there are none (data-doc §7).
func marshalToolCalls(calls []core.ToolCall) (any, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(calls)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// unmarshalToolCalls decodes the tool_calls column back into []core.ToolCall,
// returning nil for a NULL column.
func unmarshalToolCalls(v sql.NullString) ([]core.ToolCall, error) {
	if !v.Valid {
		return nil, nil
	}
	var calls []core.ToolCall
	if err := json.Unmarshal([]byte(v.String), &calls); err != nil {
		return nil, err
	}
	return calls, nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(timeLayout, s)
}
