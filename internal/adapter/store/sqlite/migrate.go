package sqlite

import (
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
)

// migrationsFS is embedded by sqlite.go (//go:embed migrations/*.sql) and
// passed here so migrate stays a pure function over an *sql.DB and an fs.FS.

// migrate applies every embedded migration whose ordinal is greater than the
// database's current PRAGMA user_version, in ascending filename order, each
// inside its own transaction that also advances user_version — so a failure
// rolls back cleanly and the DB is never left half-migrated. Already-applied
// migrations are skipped, making a repeat call a no-op (data-doc §9).
func migrate(db *sql.DB, migrations fs.FS) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("sqlite: read user_version: %w", err)
	}

	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("sqlite: glob migrations: %w", err)
	}
	sort.Strings(files)

	for i, f := range files {
		ordinal := i + 1
		if ordinal <= version {
			continue
		}

		stmt, err := fs.ReadFile(migrations, f)
		if err != nil {
			return fmt.Errorf("sqlite: read migration %s: %w", f, err)
		}

		if err := applyMigration(db, string(stmt), ordinal); err != nil {
			return fmt.Errorf("sqlite: migration %s: %w", f, err)
		}
	}

	return nil
}

// applyMigration runs one migration's SQL and advances PRAGMA user_version
// inside a single transaction. The user_version value is interpolated as a
// literal (SQLite's PRAGMA statements do not accept bind parameters) rather
// than bound: it is an adapter-controlled integer ordinal, never
// user/model-supplied content, so this is the one permitted exception to
// SR-6's "parameterized queries only".
func applyMigration(db *sql.DB, stmt string, ordinal int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}

	if _, err := tx.Exec(stmt); err != nil {
		_ = tx.Rollback()
		return err
	}

	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", ordinal)); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}
