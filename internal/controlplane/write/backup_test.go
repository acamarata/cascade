package write

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupCreatedOnSuccessfulSet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "orch.json")
	if err := os.WriteFile(path, []byte(`{"old":"data"}`), 0644); err != nil {
		t.Fatal(err)
	}

	err := Set(path, []byte(`{"new":"data"}`))
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	bakPath := path + ".bak"
	if _, err := os.Stat(bakPath); errors.Is(err, os.ErrNotExist) {
		t.Errorf("backup file %s does not exist", bakPath)
	}
}

func TestBackupContainsPreviousContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "orch.json")
	oldContent := []byte(`{"k":"v"}`)
	if err := os.WriteFile(path, oldContent, 0644); err != nil {
		t.Fatal(err)
	}

	err := Set(path, []byte(`{"new":"data"}`))
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	bakPath := path + ".bak"
	bakContent, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf("failed to read backup: %v", err)
	}

	if string(bakContent) != string(oldContent) {
		t.Errorf("backup content = %q, want %q", bakContent, oldContent)
	}
}

func TestSetFailsClosedWhenReadErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "orch.json")

	// Create a directory where the file should be, causing ReadFile to fail with IsDir (not ErrNotExist)
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}

	err := Set(path, []byte(`{"new":"data"}`))
	if err == nil {
		t.Fatalf("Set should have failed, but returned nil")
	}

	// Check that no new file was written (it's still a directory)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("path is no longer a directory, changes were applied")
	}

	// Check no backup created
	if _, err := os.Stat(path + ".bak"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("backup file should not exist")
	}
}

func TestNoBackupForNewConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "orch.json")

	err := Set(path, []byte(`{"new":"data"}`))
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	bakPath := path + ".bak"
	if _, err := os.Stat(bakPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("backup file %s should not exist", bakPath)
	}
}
