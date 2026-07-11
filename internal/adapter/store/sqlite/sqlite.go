// Package sqlite implements core.SessionRepository behind a SQLite database
// via modernc.org/sqlite (pure Go, CGO-free). Schema, PRAGMAs, and the
// migration mechanism follow docs/data/first-slice-schema.md exactly; core
// never sees SQL (SR-6).
package sqlite

import (
	"database/sql"
	"embed"
	"fmt"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// dsnTemplate sets the three connection-level PRAGMAs (data-doc §8) on every
// connection via modernc's `_pragma` DSN parameters, so no per-query setup
// and no forgotten pooled connection can violate them.
const dsnTemplate = "file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"

// Repo is a SQLite-backed core.SessionRepository.
type Repo struct {
	db *sql.DB
}

// New opens (creating if absent) the SQLite database at path, applies the
// WAL/foreign_keys/busy_timeout PRAGMAs to every connection, runs any
// pending migrations, and returns a Repo ready to serve core.SessionRepository.
//
// SetMaxOpenConns(1) is a deliberate single-writer choice (data-doc §8): it
// serializes all writes so the correlated-subquery seq assignment in
// AppendMessage is race-free without extra locking, and it costs nothing for
// this single-user, single-process tool.
func New(path string) (*Repo, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf(dsnTemplate, path))
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)

	if err := migrate(db, migrationsFS); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: migrate %s: %w", path, err)
	}

	return &Repo{db: db}, nil
}

// Close releases the underlying database connection.
func (r *Repo) Close() error {
	return r.db.Close()
}
