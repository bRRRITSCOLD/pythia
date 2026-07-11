package sqlite

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// openTempDB opens a fresh, unmigrated modernc SQLite database in a t.TempDir()
// file, with the same connection-level PRAGMAs the adapter uses in production
// (data-doc §8), so migrator tests exercise the real engine end to end.
func openTempDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "migrate.db")
	db, err := sql.Open("sqlite", fmt.Sprintf(dsnTemplate, path))
	if err != nil {
		t.Fatalf("open temp db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestMigrate_FreshDB_CreatesSchemaAndSetsUserVersion(t *testing.T) {
	db := openTempDB(t)

	if err := migrate(db, migrationsFS); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != 1 {
		t.Fatalf("user_version = %d, want 1", version)
	}

	var tableCount int
	err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('sessions', 'messages')`,
	).Scan(&tableCount)
	if err != nil {
		t.Fatalf("count tables: %v", err)
	}
	if tableCount != 2 {
		t.Fatalf("table count = %d, want 2 (sessions, messages)", tableCount)
	}
}

func TestMigrate_AlreadyMigrated_IsNoOp(t *testing.T) {
	db := openTempDB(t)

	if err := migrate(db, migrationsFS); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if err := migrate(db, migrationsFS); err != nil {
		t.Fatalf("second migrate (expected no-op): %v", err)
	}

	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != 1 {
		t.Fatalf("user_version = %d, want 1 after no-op re-run", version)
	}
}
