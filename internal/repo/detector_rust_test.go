package repo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRustDetectorFixture(t *testing.T) {
	facts, err := (rustDetector{}).Detect(context.Background(), "testdata/fixture-rust")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !facts.Detected || facts.Language != LanguageRust {
		t.Fatalf("facts = %+v, want Detected=true Language=rust", facts)
	}
	if facts.Commands.Build != "cargo build" || facts.Commands.Test != "cargo test" || facts.Commands.Lint != "cargo clippy" {
		t.Errorf("unexpected default commands: %+v", facts.Commands)
	}
}

func TestRustDetectorAbsent(t *testing.T) {
	facts, err := (rustDetector{}).Detect(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if facts.Detected {
		t.Error("Detected = true for a tree with no Cargo.toml")
	}
}

func TestRustDetectorMalformed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package\nname = "), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (rustDetector{}).Detect(context.Background(), dir); err == nil {
		t.Fatal("Detect: want error for malformed Cargo.toml, got nil")
	}
}
