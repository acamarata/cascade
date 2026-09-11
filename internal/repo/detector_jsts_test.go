package repo

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestJSTSDetectorFixture(t *testing.T) {
	facts, err := (jstsDetector{}).Detect(context.Background(), "testdata/fixture-jsts")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !facts.Detected || facts.Language != LanguageJSTS {
		t.Fatalf("facts = %+v, want Detected=true Language=jsts", facts)
	}
	if facts.Commands.Package != "pnpm" {
		t.Errorf("Package = %q, want pnpm (pnpm-lock.yaml present)", facts.Commands.Package)
	}
	found := false
	for _, e := range facts.Evidence {
		if e == "pnpm-lock.yaml" {
			found = true
		}
	}
	if !found {
		t.Errorf("Evidence = %v, want pnpm-lock.yaml listed", facts.Evidence)
	}
}

func TestJSTSDetectorNoLockfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	facts, err := (jstsDetector{}).Detect(context.Background(), dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !facts.Detected {
		t.Error("Detected = false for package.json with no lockfile, want true")
	}
	if len(facts.Evidence) != 1 || facts.Evidence[0] != "package.json" {
		t.Errorf("Evidence = %v, want [package.json]", facts.Evidence)
	}
}

func TestJSTSDetectorMalformed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (jstsDetector{}).Detect(context.Background(), dir); err == nil {
		t.Fatal("Detect: want error for malformed package.json, got nil")
	}
}

func TestJSTSDetectorReadError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod semantics differ on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "package.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	if _, err := (jstsDetector{}).Detect(context.Background(), dir); err == nil {
		t.Fatal("Detect: want error for an unreadable package.json, got nil")
	}
}

func TestJSTSDetectorAbsent(t *testing.T) {
	facts, err := (jstsDetector{}).Detect(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if facts.Detected {
		t.Error("Detected = true for a tree with no package.json")
	}
}
