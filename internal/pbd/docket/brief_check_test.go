package docket

import (
	"errors"
	"testing"
)

func TestValidateBriefConcrete(t *testing.T) {
	b := Brief{
		Files: []string{"internal/pbd/docket/x.go"},
		Funcs: []string{"Foo"},
		Types: []string{"Bar"},
		Tests: []string{"TestFoo"},
	}
	if err := ValidateBrief(b); err != nil {
		t.Fatalf("ValidateBrief returned unexpected error: %v", err)
	}
}

func TestValidateBriefMissingFile(t *testing.T) {
	b := Brief{
		Files: nil,
		Funcs: []string{"Foo"},
		Types: []string{"Bar"},
		Tests: []string{"TestFoo"},
	}
	err := ValidateBrief(b)
	if !errors.Is(err, ErrBriefMissingIdent) {
		t.Fatalf("expected ErrBriefMissingIdent for missing file, got %v", err)
	}
}

func TestValidateBriefMissingTest(t *testing.T) {
	b := Brief{
		Files: []string{"internal/pbd/docket/x.go"},
		Funcs: []string{"Foo"},
		Types: []string{"Bar"},
		Tests: nil,
	}
	err := ValidateBrief(b)
	if !errors.Is(err, ErrBriefMissingIdent) {
		t.Fatalf("expected ErrBriefMissingIdent for missing test, got %v", err)
	}
}

func TestValidateBriefVaguePlaceholder(t *testing.T) {
	b := Brief{
		Files: []string{"internal/pbd/docket/x.go"},
		Funcs: []string{"see above"},
		Types: []string{"Bar"},
		Tests: []string{"TestFoo"},
	}
	err := ValidateBrief(b)
	if !errors.Is(err, ErrBriefVague) {
		t.Fatalf("expected ErrBriefVague for vague placeholder, got %v", err)
	}
}
