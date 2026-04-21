package state

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens (or creates) the SQLite database at path, applies any pending
// migrations, and returns a Store ready for use.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("state: empty db path")
	}

	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	dsn := "file:" + filepath.ToSlash(path) + "?" + q.Encode()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("state: open: %w", err)
	}
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("state: ping: %w", err)
	}

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("state: migrate: %w", err)
	}

	return &Store{db: db}, nil
}

// migrate applies every .sql file in migrations/ in lexical order, tracking
// progress via SQLite's PRAGMA user_version. The N-th file is applied only
// when user_version < N, and user_version is bumped to N on success.
//
// This matters because not every DDL statement is idempotent — CREATE TABLE
// IF NOT EXISTS is, but ALTER TABLE ADD COLUMN isn't. user_version lets us
// add non-idempotent migrations safely.
func migrate(db *sql.DB) error {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var current int
	if err := db.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}

	for i, name := range names {
		target := i + 1
		if current >= target {
			continue
		}
		sqlBytes, err := fs.ReadFile(migrationsFS, "migrations/"+name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if _, err := db.Exec(string(sqlBytes)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", target)); err != nil {
			return fmt.Errorf("bump user_version to %d: %w", target, err)
		}
	}
	return nil
}
