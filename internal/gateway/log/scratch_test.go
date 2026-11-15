package log

import (
	"errors"
	"os"
	"testing"
)

var errBoom = errors.New("boom")

func TestScratchRemovedOnSuccess(t *testing.T) {
	base := t.TempDir()
	var gotPath string
	err := RunWithScratch(base, func(path string) error {
		gotPath = path
		return nil
	})
	if err != nil {
		t.Fatalf("RunWithScratch returned error: %v", err)
	}
	if _, statErr := os.Stat(gotPath); !os.IsNotExist(statErr) {
		t.Fatalf("scratch dir still exists at %s", gotPath)
	}
}

func TestScratchRemovedOnError(t *testing.T) {
	base := t.TempDir()
	var gotPath string
	err := RunWithScratch(base, func(path string) error {
		gotPath = path
		return errBoom
	})
	if err != errBoom {
		t.Fatalf("expected errBoom, got %v", err)
	}
	if _, statErr := os.Stat(gotPath); !os.IsNotExist(statErr) {
		t.Fatalf("scratch dir still exists at %s", gotPath)
	}
}

func TestScratchRemovedOnPanic(t *testing.T) {
	base := t.TempDir()
	var gotPath string
	defer func() {
		_ = recover()
		if _, statErr := os.Stat(gotPath); !os.IsNotExist(statErr) {
			t.Fatalf("scratch dir still exists at %s after panic", gotPath)
		}
	}()
	_ = RunWithScratch(base, func(path string) error {
		gotPath = path
		panic("test panic")
	})
}

func TestScratchReturnsFuncError(t *testing.T) {
	base := t.TempDir()
	err := RunWithScratch(base, func(_ string) error {
		return errBoom
	})
	if err != errBoom {
		t.Fatalf("expected errBoom, got %v", err)
	}
}
