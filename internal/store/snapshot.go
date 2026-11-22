package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure Go SQLite driver
)

// SchemaVersion is the schema version this binary migrates databases to.
func SchemaVersion() int { return len(migrations) }

// Snapshot writes a consistent copy of the database to path, which must not
// exist yet. It may run while the gateway writes: SQLite copies one
// read transaction.
func (s *Store) Snapshot(ctx context.Context, path string) error {
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("kopie van de database: %w", err)
	}
	return os.Chmod(path, 0o600)
}

// FileSchemaVersion returns the schema version of the database file at
// path without migrating it.
func FileSchemaVersion(ctx context.Context, path string) (int, error) {
	if _, err := os.Stat(path); err != nil {
		return 0, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var v int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("database niet leesbaar: %w", err)
	}
	return v, nil
}
