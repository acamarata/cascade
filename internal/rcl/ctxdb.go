// Package rcl provides context database functionality for Cascade.
package rcl

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// CtxDB represents a context database connection.
type CtxDB struct {
	DB *sql.DB
}

// CtxHit represents a search hit from the context database.
type CtxHit struct {
	Type string
	ID   string
	Text string
}

var (
	// ErrCtxDBInvalid is returned when invalid input is provided to CtxDB operations.
	ErrCtxDBInvalid = errors.New("rcl: invalid ctxdb input")
	// ErrCtxDBDuplicate is returned when a duplicate ID is encountered.
	ErrCtxDBDuplicate = errors.New("rcl: duplicate ctxdb id")
)

// OpenCtxDB opens or creates a context database at the given path.
// The path must be an absolute path; relative paths, blank paths, and tilde-prefixed paths return ErrCtxDBInvalid.
// Missing parent directories are created with 0700 permissions.
// Opening an existing database is idempotent.
func OpenCtxDB(path string) (*CtxDB, error) {
	if path == "" || strings.TrimSpace(path) != path || strings.HasPrefix(path, "~") || !filepath.IsAbs(path) {
		return nil, ErrCtxDBInvalid
	}

	cleanPath := filepath.Clean(path)

	// Create parent directories if they don't exist
	parentDir := filepath.Dir(cleanPath)
	if err := os.MkdirAll(parentDir, 0700); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", cleanPath)
	if err != nil {
		return nil, err
	}

	// Set single connection
	db.SetMaxOpenConns(1)

	ctxDB := &CtxDB{DB: db}

	if err := ctxDB.createSchema(); err != nil {
		db.Close()
		return nil, err
	}

	return ctxDB, nil
}

// Close closes the database connection.
// Returns ErrCtxDBInvalid if the receiver or DB is nil.
func (db *CtxDB) Close() error {
	if db == nil || db.DB == nil {
		return ErrCtxDBInvalid
	}
	return db.DB.Close()
}

// createSchema creates the required tables and FTS5 virtual tables in a transaction.
func (db *CtxDB) createSchema() error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	tables := []string{"segments", "blobs", "sections"}
	for _, tbl := range tables {
		createTable := `CREATE TABLE IF NOT EXISTS ` + tbl + ` (
			id INTEGER PRIMARY KEY,
			external_id TEXT NOT NULL UNIQUE,
			text TEXT NOT NULL
		)`
		if _, err := tx.Exec(createTable); err != nil {
			return err
		}

		createFTS := `CREATE VIRTUAL TABLE IF NOT EXISTS ` + tbl + `_fts USING fts5(text, content='')`
		if _, err := tx.Exec(createFTS); err != nil {
			return err
		}
	}

	return tx.Commit()
}
