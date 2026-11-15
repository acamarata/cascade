package atlas

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestWriteBodyToFTS validates WriteBody behavior for various inputs and home policies.
func TestWriteBodyToFTS(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "notes.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	// Create the notes table and notes_fts table
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS notes (
			id INTEGER PRIMARY KEY,
			title TEXT
		);
		CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts USING fts5(body);
	`)
	if err != nil {
		t.Fatalf("failed to create tables: %v", err)
	}

	// Insert a test note
	_, err = db.Exec("INSERT INTO notes (id, title) VALUES (1, 'Test Note')")
	if err != nil {
		t.Fatalf("failed to insert note: %v", err)
	}

	t.Run("nil db returns ErrBodyInput", func(t *testing.T) {
		err := WriteBody(nil, 1, "hello", map[int64]string{1: "sqlite"})
		if !errors.Is(err, ErrBodyInput) {
			t.Errorf("expected ErrBodyInput, got %v", err)
		}
	})

	t.Run("nonpositive id returns ErrBodyInput", func(t *testing.T) {
		err := WriteBody(db, 0, "hello", map[int64]string{1: "sqlite"})
		if !errors.Is(err, ErrBodyInput) {
			t.Errorf("expected ErrBodyInput, got %v", err)
		}
		err = WriteBody(db, -1, "hello", map[int64]string{1: "sqlite"})
		if !errors.Is(err, ErrBodyInput) {
			t.Errorf("expected ErrBodyInput, got %v", err)
		}
	})

	t.Run("missing note returns ErrBodyNotFound", func(t *testing.T) {
		err := WriteBody(db, 999, "hello", map[int64]string{999: "sqlite"})
		if !errors.Is(err, ErrBodyNotFound) {
			t.Errorf("expected ErrBodyNotFound, got %v", err)
		}
	})

	t.Run("nil homes returns ErrFTSBodyPolicyRequired for existing note", func(t *testing.T) {
		err := WriteBody(db, 1, "hello", nil)
		if !errors.Is(err, ErrFTSBodyPolicyRequired) {
			t.Errorf("expected ErrFTSBodyPolicyRequired, got %v", err)
		}
	})

	t.Run("empty homes returns ErrFTSBodyPolicyRequired for existing note", func(t *testing.T) {
		err := WriteBody(db, 1, "hello", map[int64]string{})
		if !errors.Is(err, ErrFTSBodyPolicyRequired) {
			t.Errorf("expected ErrFTSBodyPolicyRequired, got %v", err)
		}
	})

	t.Run("missing key in homes returns ErrFTSBodyPolicyRequired for existing note", func(t *testing.T) {
		err := WriteBody(db, 1, "hello", map[int64]string{2: "sqlite"})
		if !errors.Is(err, ErrFTSBodyPolicyRequired) {
			t.Errorf("expected ErrFTSBodyPolicyRequired, got %v", err)
		}
	})

	t.Run("invalid home value returns ErrBodyInput", func(t *testing.T) {
		err := WriteBody(db, 1, "hello", map[int64]string{1: "unknown"})
		if !errors.Is(err, ErrBodyInput) {
			t.Errorf("expected ErrBodyInput, got %v", err)
		}
	})

	t.Run("whitespace home returns ErrBodyInput", func(t *testing.T) {
		err := WriteBody(db, 1, "hello", map[int64]string{1: "  sqlite  "})
		if !errors.Is(err, ErrBodyInput) {
			t.Errorf("expected ErrBodyInput, got %v", err)
		}
	})

	t.Run("differently cased home returns ErrBodyInput", func(t *testing.T) {
		err := WriteBody(db, 1, "hello", map[int64]string{1: "SQLite"})
		if !errors.Is(err, ErrBodyInput) {
			t.Errorf("expected ErrBodyInput, got %v", err)
		}
	})

	t.Run("nonpositive map key returns ErrBodyInput", func(t *testing.T) {
		err := WriteBody(db, 1, "hello", map[int64]string{0: "sqlite"})
		if !errors.Is(err, ErrBodyInput) {
			t.Errorf("expected ErrBodyInput, got %v", err)
		}
		err = WriteBody(db, 1, "hello", map[int64]string{-1: "sqlite"})
		if !errors.Is(err, ErrBodyInput) {
			t.Errorf("expected ErrBodyInput, got %v", err)
		}
	})

	t.Run("git home returns ErrFTSBodyProhibited without modifying rows", func(t *testing.T) {
		// Check row count before
		var countBefore int
		err := db.QueryRow("SELECT COUNT(*) FROM notes_fts").Scan(&countBefore)
		if err != nil {
			t.Fatalf("failed to count rows: %v", err)
		}

		err = WriteBody(db, 1, "hello", map[int64]string{1: "git"})
		if !errors.Is(err, ErrFTSBodyProhibited) {
			t.Errorf("expected ErrFTSBodyProhibited, got %v", err)
		}

		// Check row count after - should be unchanged
		var countAfter int
		err = db.QueryRow("SELECT COUNT(*) FROM notes_fts").Scan(&countAfter)
		if err != nil {
			t.Fatalf("failed to count rows: %v", err)
		}
		if countBefore != countAfter {
			t.Errorf("row count changed from %d to %d", countBefore, countAfter)
		}
	})

	t.Run("archive home returns ErrFTSBodyProhibited without modifying rows", func(t *testing.T) {
		// Check row count before
		var countBefore int
		err := db.QueryRow("SELECT COUNT(*) FROM notes_fts").Scan(&countBefore)
		if err != nil {
			t.Fatalf("failed to count rows: %v", err)
		}

		err = WriteBody(db, 1, "hello", map[int64]string{1: "archive"})
		if !errors.Is(err, ErrFTSBodyProhibited) {
			t.Errorf("expected ErrFTSBodyProhibited, got %v", err)
		}

		// Check row count after - should be unchanged
		var countAfter int
		err = db.QueryRow("SELECT COUNT(*) FROM notes_fts").Scan(&countAfter)
		if err != nil {
			t.Fatalf("failed to count rows: %v", err)
		}
		if countBefore != countAfter {
			t.Errorf("row count changed from %d to %d", countBefore, countAfter)
		}
	})

	t.Run("sqlite home writes body to FTS", func(t *testing.T) {
		err := WriteBody(db, 1, "hello", map[int64]string{1: "sqlite"})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}

		// Verify the body was written
		var body string
		err = db.QueryRow("SELECT body FROM notes_fts WHERE rowid = 1").Scan(&body)
		if err != nil {
			t.Errorf("failed to query notes_fts: %v", err)
		}
		if body != "hello" {
			t.Errorf("expected body 'hello', got '%s'", body)
		}
	})

	t.Run("empty body is valid and writes to FTS", func(t *testing.T) {
		err := WriteBody(db, 1, "", map[int64]string{1: "sqlite"})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}

		// Verify the empty body was written
		var body string
		err = db.QueryRow("SELECT body FROM notes_fts WHERE rowid = 1").Scan(&body)
		if err != nil {
			t.Errorf("failed to query notes_fts: %v", err)
		}
		if body != "" {
			t.Errorf("expected empty body, got '%s'", body)
		}
	})

	t.Run("sqlite home replaces existing body", func(t *testing.T) {
		err := WriteBody(db, 1, "first", map[int64]string{1: "sqlite"})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}

		err = WriteBody(db, 1, "second", map[int64]string{1: "sqlite"})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}

		// Verify the body was replaced
		var body string
		err = db.QueryRow("SELECT body FROM notes_fts WHERE rowid = 1").Scan(&body)
		if err != nil {
			t.Errorf("failed to query notes_fts: %v", err)
		}
		if body != "second" {
			t.Errorf("expected body 'second', got '%s'", body)
		}
	})
}
