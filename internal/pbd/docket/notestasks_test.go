package docket

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNotesTasksListsAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.md", "- [ ] task 1\n")
	writeFile(t, dir, "b.md", "- [ ] task 2\n- [x] task 3\n")

	entries, err := NotesTasks(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].Path != "a.md" || entries[1].Path != "b.md" || entries[2].Path != "b.md" {
		t.Errorf("unexpected paths: %v, %v, %v", entries[0].Path, entries[1].Path, entries[2].Path)
	}
}

func TestNotesTasksLineNumbers(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.md", "line 1\nline 2\n- [ ] task on line 3\n")

	entries, err := NotesTasks(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Path != "a.md" {
		t.Errorf("expected path a.md, got %s", entries[0].Path)
	}
	if entries[0].Line != 3 {
		t.Errorf("expected line 3, got %d", entries[0].Line)
	}
}

func TestNotesTasksIgnoresNonTasks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.md", "plain text\n- bullet point\n  - nested bullet\n")

	entries, err := NotesTasks(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

func TestNotesTasksMissingDirFailsClosed(t *testing.T) {
	entries, err := NotesTasks(filepath.Join(t.TempDir(), "nonexistent"))
	if !errors.Is(err, ErrNotesDir) {
		t.Errorf("expected ErrNotesDir, got %v", err)
	}
	if entries != nil {
		t.Errorf("expected nil entries, got %v", entries)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
}
