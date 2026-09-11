package repo

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGoDetectorFixture(t *testing.T) {
	facts, err := (goDetector{}).Detect(context.Background(), "testdata/fixture-go")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !facts.Detected || facts.Language != LanguageGo {
		t.Fatalf("facts = %+v, want Detected=true Language=go", facts)
	}
	if facts.Commands.Build != "go build ./..." || facts.Commands.Test != "go test ./..." || facts.Commands.Lint != "golangci-lint run" {
		t.Errorf("unexpected default commands: %+v", facts.Commands)
	}
	if len(facts.Evidence) != 1 || facts.Evidence[0] != "go.mod" {
		t.Errorf("Evidence = %v, want [go.mod]", facts.Evidence)
	}
}

func TestGoDetectorAbsent(t *testing.T) {
	facts, err := (goDetector{}).Detect(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if facts.Detected {
		t.Error("Detected = true for a tree with no go.mod")
	}
}

func TestGoDetectorMalformed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("this is not { a go.mod"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := (goDetector{}).Detect(context.Background(), dir)
	if err == nil {
		t.Fatal("Detect: want error for malformed go.mod, got nil")
	}
}

func TestGoDetectorReadError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod semantics differ on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(path, []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	if _, err := (goDetector{}).Detect(context.Background(), dir); err == nil {
		t.Fatal("Detect: want error for an unreadable go.mod, got nil")
	}
}
