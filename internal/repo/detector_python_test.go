package repo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPythonDetectorPyproject(t *testing.T) {
	facts, err := (pythonDetector{}).Detect(context.Background(), "testdata/fixture-python")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !facts.Detected || facts.Language != LanguagePython {
		t.Fatalf("facts = %+v, want Detected=true Language=python", facts)
	}
	if len(facts.Evidence) != 1 || facts.Evidence[0] != "pyproject.toml" {
		t.Errorf("Evidence = %v, want [pyproject.toml]", facts.Evidence)
	}
}

func TestPythonDetectorRequirements(t *testing.T) {
	facts, err := (pythonDetector{}).Detect(context.Background(), "testdata/fixture-python-req")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !facts.Detected || facts.Commands.Test != "pytest" {
		t.Fatalf("facts = %+v, want Detected=true test=pytest", facts)
	}
	if len(facts.Evidence) != 1 || facts.Evidence[0] != "requirements.txt" {
		t.Errorf("Evidence = %v, want [requirements.txt]", facts.Evidence)
	}
}

func TestPythonDetectorAbsent(t *testing.T) {
	facts, err := (pythonDetector{}).Detect(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if facts.Detected {
		t.Error("Detected = true for a tree with no python markers")
	}
}

func TestPythonDetectorMalformedPyproject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project\nname="), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (pythonDetector{}).Detect(context.Background(), dir); err == nil {
		t.Fatal("Detect: want error for malformed pyproject.toml, got nil")
	}
}
