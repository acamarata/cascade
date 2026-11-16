package ledger

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSportDocsEmptyTranscript(t *testing.T) {
	// Test blank path
	data, err := SportDocs("")
	if data != nil {
		t.Errorf("expected nil data for empty path, got %v", data)
	}
	if !errors.Is(err, ErrEmptyTranscript) {
		t.Errorf("expected ErrEmptyTranscript for empty path, got %v", err)
	}

	// Create a temporary directory for file tests
	tmpDir := t.TempDir()

	// Test zero-byte file
	zeroFile := filepath.Join(tmpDir, "empty.txt")
	if err := os.WriteFile(zeroFile, []byte{}, 0644); err != nil {
		t.Fatalf("failed to create zero-byte file: %v", err)
	}
	data, err = SportDocs(zeroFile)
	if data != nil {
		t.Errorf("expected nil data for zero-byte file, got %v", data)
	}
	if !errors.Is(err, ErrEmptyTranscript) {
		t.Errorf("expected ErrEmptyTranscript for zero-byte file, got %v", err)
	}

	// Test whitespace-only file
	wsFile := filepath.Join(tmpDir, "whitespace.txt")
	if err := os.WriteFile(wsFile, []byte("   \n\t  "), 0644); err != nil {
		t.Fatalf("failed to create whitespace file: %v", err)
	}
	data, err = SportDocs(wsFile)
	if data != nil {
		t.Errorf("expected nil data for whitespace-only file, got %v", data)
	}
	if !errors.Is(err, ErrEmptyTranscript) {
		t.Errorf("expected ErrEmptyTranscript for whitespace-only file, got %v", err)
	}
}

func TestSportDocsInvalidPath(t *testing.T) {
	// Test nonexistent file
	data, err := SportDocs("/nonexistent/path/file.txt")
	if data != nil {
		t.Errorf("expected nil data for nonexistent path, got %v", data)
	}
	if !errors.Is(err, ErrInvalidPath) {
		t.Errorf("expected ErrInvalidPath for nonexistent path, got %v", err)
	}

	// Test directory path
	tmpDir := t.TempDir()
	data, err = SportDocs(tmpDir)
	if data != nil {
		t.Errorf("expected nil data for directory path, got %v", data)
	}
	if !errors.Is(err, ErrInvalidPath) {
		t.Errorf("expected ErrInvalidPath for directory path, got %v", err)
	}
}
