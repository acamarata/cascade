package docket

import (
	"errors"
	"testing"
)

func TestValidPath(t *testing.T) {
	if err := ValidateScopeEntry("src/main.go"); err != nil {
		t.Fatalf("ValidateScopeEntry(%q) = %v, want nil", "src/main.go", err)
	}
}

func TestValidGlob(t *testing.T) {
	if err := ValidateScopeEntry("src/**/*.go"); err != nil {
		t.Fatalf("ValidateScopeEntry(%q) = %v, want nil", "src/**/*.go", err)
	}
}

func TestNoteInParentheses(t *testing.T) {
	if err := ValidateScopeEntry("src/main.go (entrypoint)"); err != nil {
		t.Fatalf("ValidateScopeEntry(%q) = %v, want nil", "src/main.go (entrypoint)", err)
	}
}

func TestNoteAfterDash(t *testing.T) {
	if err := ValidateScopeEntry("src/main.go - entrypoint"); err != nil {
		t.Fatalf("ValidateScopeEntry(%q) = %v, want nil", "src/main.go - entrypoint", err)
	}
}

func TestRejectRawSpace(t *testing.T) {
	err := ValidateScopeEntry("src/my file.go")
	if !errors.Is(err, ErrScope) {
		t.Fatalf("ValidateScopeEntry(%q) = %v, want %v", "src/my file.go", err, ErrScope)
	}
}

func TestRejectEmpty(t *testing.T) {
	err := ValidateScopeEntry("")
	if !errors.Is(err, ErrScope) {
		t.Fatalf("ValidateScopeEntry(%q) = %v, want %v", "", err, ErrScope)
	}
}
