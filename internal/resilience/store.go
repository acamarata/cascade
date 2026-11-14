// Package resilience provides types and interfaces for tracking process health.
package resilience

import (
	"database/sql"
	"errors"
	"time"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/runtime"
)

// clock is the time source for the store.
var clock runtime.Clock = runtime.NewSystemClock()

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

// waitKindString returns the string representation of a WaitKind.
func waitKindString(kind WaitKind) (string, error) {
	switch kind {
	case WaitingDisk:
		return "waiting_disk", nil
	case WaitingNet:
		return "waiting_net", nil
	case WaitingModels:
		return "waiting_models", nil
	case WaitingOwner:
		return "waiting_owner", nil
	default:
		return "", errors.New("unknown WaitKind")
	}
}

// SetWait records that the process is waiting for the given kind of resource.
// It upserts into the wait table setting since to clock.Now().UTC(), keeping
// existing since if the row already exists for that kind.
func (s *DBStore) SetWait(kind WaitKind) error {
	_, err := waitKindString(kind)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		INSERT INTO wait (kind, since) VALUES (?, ?)
		ON CONFLICT(kind) DO NOTHING`,
		int(kind), clock.Now().UTC())
	return err
}

// ClearWait removes the wait condition for the given kind.
// It reads since from the wait table, computes elapsed duration,
// writes elapsed to meta table as last_wait_<kind>_duration,
// then deletes the wait row.
func (s *DBStore) ClearWait(kind WaitKind) error {
	name, err := waitKindString(kind)
	if err != nil {
		return err
	}

	var since time.Time
	err = s.db.QueryRow("SELECT since FROM wait WHERE kind=?", int(kind)).Scan(&since)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}

	elapsed := clock.Now().Sub(since)
	metaKey := "last_wait_" + name + "_duration"

	_, err = s.db.Exec("INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)", metaKey, elapsed.String())
	if err != nil {
		return err
	}

	_, err = s.db.Exec("DELETE FROM wait WHERE kind=?", int(kind))
	return err
}

// RecordHeartbeat records a heartbeat signal.
func (s *DBStore) RecordHeartbeat() error {
	_, err := s.db.Exec("INSERT INTO heartbeat (at) VALUES (?)", clock.Now().UTC())
	return err
}

// MarkSeenRunning marks the process as seen running.
func (s *DBStore) MarkSeenRunning() error {
	_, err := s.db.Exec("INSERT INTO seen_running (at) VALUES (?)", clock.Now().UTC())
	return err
}

// NextRestartDelay returns the delay before the next restart attempt.
// It reads the current restart count, computes an exponential backoff
// (1, 2, 4, 8, 16, 30 minutes), increments the count and updates the
// last restart timestamp. If no restart record exists, it initializes one.
func (s *DBStore) NextRestartDelay() time.Duration {
	var count int
	err := s.db.QueryRow("SELECT count FROM restart LIMIT 1").Scan(&count)
	if err == sql.ErrNoRows {
		_, err = s.db.Exec("INSERT INTO restart (count, last) VALUES (0, ?)", clock.Now().UTC())
		if err != nil {
			return 0
		}
		count = 0
	} else if err != nil {
		return 0
	}

	delay := 1 << count
	if delay > 30 {
		delay = 30
	}
	dur := time.Duration(delay) * time.Minute

	_, err = s.db.Exec("UPDATE restart SET count = count + 1, last = ?", clock.Now().UTC())
	if err != nil {
		return 0
	}

	return dur
}

// RecordStall records a stall condition with the given reason.
func (s *DBStore) RecordStall(reason string) error {
	_, err := s.db.Exec("INSERT INTO stall (reason, at) VALUES (?, ?)", reason, clock.Now().UTC())
	return err
}

// EmitEvent records a lifecycle event.
func (s *DBStore) EmitEvent(ev Event) error {
	at := ev.At
	if at.IsZero() {
		at = clock.Now().UTC()
	} else {
		at = at.UTC()
	}
	_, err := s.db.Exec("INSERT INTO event (at) VALUES (?)", at)
	return err
}

// TrimEvents removes old events to keep the store size bounded.
func (s *DBStore) TrimEvents() error {
	_, err := s.db.Exec("DELETE FROM event WHERE at < datetime('now', '-14 days')")
	return err
}

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
		_ = db.Close()
		return nil, err
	}

	_, err = db.Exec("PRAGMA synchronous=FULL")
	if err != nil {
		_ = db.Close()
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
			_ = db.Close()
			return nil, err
		}
	}

	return &DBStore{db: db}, nil
}
