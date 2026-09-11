package repo

import (
	"context"
	"testing"
)

func TestGenericDetectorMakefile(t *testing.T) {
	facts, err := (genericDetector{}).Detect(context.Background(), "testdata/fixture-generic")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !facts.Detected || facts.Language != LanguageGeneric {
		t.Fatalf("facts = %+v, want Detected=true Language=generic", facts)
	}
	if facts.Commands.Build != "make build" || facts.Commands.Test != "make test" || facts.Commands.Lint != "make lint" {
		t.Errorf("unexpected commands: %+v", facts.Commands)
	}
}

func TestGenericDetectorEmptyTree(t *testing.T) {
	facts, err := (genericDetector{}).Detect(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !facts.Detected {
		t.Fatal("Detected = false, generic must always detect")
	}
	if facts.Commands.Build != "" || facts.Commands.Test != "" || facts.Commands.Lint != "" {
		t.Errorf("commands = %+v, want all empty (no Makefile, no guessing)", facts.Commands)
	}
	if len(facts.Evidence) != 0 {
		t.Errorf("Evidence = %v, want empty", facts.Evidence)
	}
}
