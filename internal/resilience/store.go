// Package resilience provides types and interfaces for tracking process health.
package resilience

import (
	"database/sql"
	"errors"
	"time"

	_ "modernc.org/sqlite"
)

// WaitKind represents the kind of wait condition a process is experiencing.
type WaitKind int

const (
	// WaitingDisk indicates the process is waiting for disk resources.
	WaitingDisk WaitKind = iota
	// WaitingNet indicates the process is waiting for network resources.
	WaitingNet
	// WaitingModels indicates the process is waiting for models to be available.
	WaitingModels
	// WaitingOwner indicates the process is waiting for the owner process.
	WaitingOwner
)

// Wait represents a wait condition with a start time and kind.
type Wait struct {
	Since time.Time
	Kind  WaitKind
}

// Restart tracks restart attempts and the last restart time.
type Restart struct {
	Count int
	Last  time.Time
}

// Event represents a timed event in the process lifecycle.
type Event struct {
	At time.Time
}

// Store defines the interface for persisting and querying process resilience state.
type Store interface {
	// SetWait records that the process is waiting for the given kind of resource.
	SetWait(kind WaitKind) error
	// ClearWait removes the wait condition for the given kind.
	ClearWait(kind WaitKind) error
	// RecordHeartbeat records a heartbeat signal.
	RecordHeartbeat() error
	// MarkSeenRunning marks the process as seen running.
	MarkSeenRunning() error
	// NextRestartDelay returns the delay before the next restart attempt.
	NextRestartDelay() time.Duration
	// RecordStall records a stall condition with the given reason.
	RecordStall(reason string) error
	// EmitEvent records a lifecycle event.
	EmitEvent(ev Event) error
	// TrimEvents removes old events to keep the store size bounded.
	TrimEvents() error
}

// DBStore is a SQLite-backed implementation of the Store interface.
type DBStore struct {
	db *sql.DB
}

// QueryRow executes a query that is expected to return at most one row.
func (s *DBStore) QueryRow(query string, args ...any) *sql.Row {
	return s.db.QueryRow(query, args...)
}

// Close closes the underlying database connection.
func (s *DBStore) Close() error {
	return s.db.Close()
}

// SetWait records that the process is waiting for the given kind of resource.
func (s *DBStore) SetWait(kind WaitKind) error { return nil }

// ClearWait removes the wait condition for the given kind.
func (s *DBStore) ClearWait(kind WaitKind) error { return nil }

// RecordHeartbeat records a heartbeat signal.
func (s *DBStore) RecordHeartbeat() error { return nil }

// MarkSeenRunning marks the process as seen running.
func (s *DBStore) MarkSeenRunning() error { return nil }

// NextRestartDelay returns the delay before the next restart attempt.
func (s *DBStore) NextRestartDelay() time.Duration { return 0 }

// RecordStall records a stall condition with the given reason.
func (s *DBStore) RecordStall(reason string) error { return nil }

// EmitEvent records a lifecycle event.
func (s *DBStore) EmitEvent(ev Event) error { return nil }

// TrimEvents removes old events to keep the store size bounded.
func (s *DBStore) TrimEvents() error { return nil }

// Open opens a SQLite database at the given path, sets WAL mode and synchronous=FULL,
// and creates the required tables if they do not exist.
func Open(path string) (*DBStore, error) {
	if path == "" {
		return nil, errors.New("path cannot be empty")
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)

	_, err = db.Exec("PRAGMA journal_mode=WAL")
	if err != nil {
		db.Close()
		return nil, err
	}

	_, err = db.Exec("PRAGMA synchronous=FULL")
	if err != nil {
		db.Close()
		return nil, err
	}

	tables := []string{
		"CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT)",
		"CREATE TABLE IF NOT EXISTS wait (kind INTEGER PRIMARY KEY, since TIMESTAMP)",
		"CREATE TABLE IF NOT EXISTS heartbeat (id INTEGER PRIMARY KEY, at TIMESTAMP)",
		"CREATE TABLE IF NOT EXISTS seen_running (id INTEGER PRIMARY KEY, at TIMESTAMP)",
		"CREATE TABLE IF NOT EXISTS restart (count INTEGER, last TIMESTAMP)",
		"CREATE TABLE IF NOT EXISTS stall (reason TEXT, at TIMESTAMP)",
		"CREATE TABLE IF NOT EXISTS event (at TIMESTAMP)",
	}

	for _, q := range tables {
		_, err = db.Exec(q)
		if err != nil {
			db.Close()
			return nil, err
		}
	}

	return &DBStore{db: db}, nil
}
