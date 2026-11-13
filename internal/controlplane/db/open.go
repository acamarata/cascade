package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Open opens a SQLite database at the given path, sets the journal mode to WAL,
// and sets the busy timeout to 3000 milliseconds. It returns the database handle
// or an error if the database cannot be opened or the pragmas fail.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Ensure single connection for pragma persistence
	db.SetMaxOpenConns(1)

	// Verify the connection works
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Set journal mode to WAL
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to set journal_mode=WAL: %w", err)
	}

	// Set busy timeout to 3000ms
	if _, err := db.Exec("PRAGMA busy_timeout=3000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to set busy_timeout=3000: %w", err)
	}

	return db, nil
}
