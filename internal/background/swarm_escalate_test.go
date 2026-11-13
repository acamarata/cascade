package background

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEmitEscalateEvent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "escalate.jsonl")

	// Valid needs-owner
	err := WriteEscalate(path, Outcome{Status: "needs-owner", Detail: "failure"})
	if err != nil {
		t.Fatalf("WriteEscalate needs-owner: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("expected mode 0600, got %v", info.Mode().Perm())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	expected := `{"status":"needs-owner","detail":"failure"}` + "\n"
	if string(data) != expected {
		t.Fatalf("expected %q, got %q", expected, string(data))
	}

	// Valid done (no-op even if detail nonempty)
	err = WriteEscalate(path, Outcome{Status: "done", Detail: "ignored"})
	if err != nil {
		t.Fatalf("WriteEscalate done: %v", err)
	}
	data2, _ := os.ReadFile(path)
	if string(data2) != expected+`{"status":"done","detail":"ignored"}`+"\n" {
		t.Fatalf("expected append of done, got %q", string(data2))
	}

	// Repeated identical needs-owner
	err = WriteEscalate(path, Outcome{Status: "needs-owner", Detail: "failure"})
	if err != nil {
		t.Fatalf("WriteEscalate repeat: %v", err)
	}
	data3, _ := os.ReadFile(path)
	expected3 := expected + `{"status":"done","detail":"ignored"}` + "\n" + expected
	if string(data3) != expected3 {
		t.Fatalf("expected repeat append, got %q", string(data3))
	}
}

func TestEmitEscalateEventInvalidInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "escalate.jsonl")

	// Invalid path
	err := WriteEscalate("", Outcome{Status: "done"})
	if !errors.Is(err, ErrInvalidEscalation) {
		t.Fatalf("expected ErrInvalidEscalation for blank path, got %v", err)
	}

	// Invalid status
	err = WriteEscalate(path, Outcome{Status: "unknown", Detail: "x"})
	if !errors.Is(err, ErrInvalidEscalation) {
		t.Fatalf("expected ErrInvalidEscalation for unknown status, got %v", err)
	}

	// needs-owner without detail
	err = WriteEscalate(path, Outcome{Status: "needs-owner"})
	if !errors.Is(err, ErrInvalidEscalation) {
		t.Fatalf("expected ErrInvalidEscalation for needs-owner without detail, got %v", err)
	}
}

func TestEmitEscalateEventWriteFailures(t *testing.T) {
	dir := t.TempDir()

	// I/O error: directory instead of file
	dirPath := filepath.Join(dir, "subdir")
	if err := os.Mkdir(dirPath, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	err := WriteEscalate(dirPath, Outcome{Status: "done"})
	if !errors.Is(err, ErrEscalationWrite) {
		t.Fatalf("expected ErrEscalationWrite for directory, got %v", err)
	}

	// Permissions failure: existing file with broad permissions
	broadPath := filepath.Join(dir, "broad.jsonl")
	if err := os.WriteFile(broadPath, []byte(""), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	err = WriteEscalate(broadPath, Outcome{Status: "done"})
	if !errors.Is(err, ErrEscalationWrite) {
		t.Fatalf("expected ErrEscalationWrite for broad permissions, got %v", err)
	}

	// Non-existent parent directory
	badParent := filepath.Join(dir, "nonexistent", "file.jsonl")
	err = WriteEscalate(badParent, Outcome{Status: "done"})
	if !errors.Is(err, ErrEscalationWrite) {
		t.Fatalf("expected ErrEscalationWrite for bad parent, got %v", err)
	}
}
