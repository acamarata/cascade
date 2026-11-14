package hj

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClearSubstituteEmptiesFolder(t *testing.T) {
	dir := t.TempDir()
	aPath := filepath.Join(dir, "a.txt")
	bPath := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(aPath, make([]byte, 10), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bPath, make([]byte, 20), 0644); err != nil {
		t.Fatal(err)
	}

	if err := ClearSubstitute(dir); err != nil {
		t.Fatalf("ClearSubstitute failed: %v", err)
	}

	var size int64
	err := filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if size != 0 {
		t.Fatalf("expected size 0, got %d", size)
	}
}

func TestClearSubstituteEmptyStaysZero(t *testing.T) {
	dir := t.TempDir()

	if err := ClearSubstitute(dir); err != nil {
		t.Fatalf("ClearSubstitute failed: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("folder does not exist: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("folder is not a directory")
	}
}

func TestClearSubstituteRemovesNested(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	cPath := filepath.Join(subDir, "c.txt")
	if err := os.WriteFile(cPath, make([]byte, 10), 0644); err != nil {
		t.Fatal(err)
	}

	if err := ClearSubstitute(dir); err != nil {
		t.Fatalf("ClearSubstitute failed: %v", err)
	}

	if _, err := os.Stat(cPath); !os.IsNotExist(err) {
		t.Fatalf("nested file c.txt still exists")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

func TestClearSubstituteMissingDirErrors(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nonexistent")

	err := ClearSubstitute(dir)
	if err == nil {
		t.Fatal("expected error for nonexistent path, got nil")
	}

	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatalf("nonexistent path was created")
	}
}
