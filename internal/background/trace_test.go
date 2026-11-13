package background

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadTraceFromJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trace.jsonl")
	err := os.WriteFile(path, []byte(`{"id":"1","agent":"a","status":"ok"}`+"\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := ReadTrace(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tr.ID != "1" || tr.Agent != "a" || tr.Status != "ok" {
		t.Fatalf("unexpected trace: %+v", tr)
	}
}

func TestReadTraceFromMeta(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trace.meta.json")
	err := os.WriteFile(path, []byte(`{"id":"2","agent":"b","status":"pending"}`), 0644)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := ReadTrace(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tr.ID != "2" || tr.Agent != "b" || tr.Status != "pending" {
		t.Fatalf("unexpected trace: %+v", tr)
	}
}

func TestReadTraceFromTranscriptFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.txt")
	err := os.WriteFile(path, []byte("some text"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ReadTrace(path)
	if !errors.Is(err, ErrTranscriptRead) {
		t.Fatalf("expected ErrTranscriptRead, got %v", err)
	}
}

func TestSingleWriterAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-spawns.jsonl")
	tr := Trace{ID: "3", Agent: "c", Status: "running"}
	err := AppendSpawn(path, tr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDualWriteFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-spawns.jsonl")
	tr := Trace{ID: "4", Agent: "d", Status: "running"}

	err := LockWriter(path)
	if err != nil {
		t.Fatalf("unexpected error on first lock: %v", err)
	}

	err = AppendSpawn(path, tr)
	if !errors.Is(err, ErrDualWrite) {
		t.Fatalf("expected ErrDualWrite, got %v", err)
	}
}
