package docket

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRecordSplitFirstTime(t *testing.T) {
	dir := t.TempDir()
	ok, err := RecordSplit(dir, "t1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected true on first record")
	}

	path := filepath.Join(dir, "splits.jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("splits.jsonl not created: %v", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	lines := 0
	for scanner.Scan() {
		lines++
	}
	if lines != 1 {
		t.Fatalf("expected 1 line, got %d", lines)
	}
}

func TestRecordSplitRepeatIsNoop(t *testing.T) {
	dir := t.TempDir()
	ok, err := RecordSplit(dir, "t1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected true on first record")
	}

	ok, err = RecordSplit(dir, "t1")
	if err != nil {
		t.Fatalf("unexpected error on repeat: %v", err)
	}
	if ok {
		t.Fatal("expected false on repeat")
	}

	path := filepath.Join(dir, "splits.jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("splits.jsonl not found: %v", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	lines := 0
	for scanner.Scan() {
		lines++
	}
	if lines != 1 {
		t.Fatalf("expected 1 line after repeat, got %d", lines)
	}
}

func TestHasSplitReadsFile(t *testing.T) {
	dir := t.TempDir()
	_, err := RecordSplit(dir, "t1")
	if err != nil {
		t.Fatalf("unexpected error recording t1: %v", err)
	}

	has, err := HasSplit(dir, "t1")
	if err != nil {
		t.Fatalf("unexpected error checking t1: %v", err)
	}
	if !has {
		t.Fatal("expected HasSplit to return true for t1")
	}

	has, err = HasSplit(dir, "t2")
	if err != nil {
		t.Fatalf("unexpected error checking t2: %v", err)
	}
	if has {
		t.Fatal("expected HasSplit to return false for t2")
	}
}

func TestRecordSplitCorruptFileFailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "splits.jsonl")
	if err := os.WriteFile(path, []byte("not json\n"), 0644); err != nil {
		t.Fatalf("failed to write corrupt file: %v", err)
	}

	_, err := RecordSplit(dir, "t1")
	if err == nil {
		t.Fatal("expected error for corrupt file, got nil")
	}
	if !errors.Is(err, ErrSplitsCorrupt) {
		t.Fatalf("expected ErrSplitsCorrupt, got %v", err)
	}
}
