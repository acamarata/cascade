package modname

import (
	"errors"
	"testing"
)

func TestValidateGoodName(t *testing.T) {
	err := Validate("Redact", []string{"Accept"}, []string{"Redact"})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestValidateTwoWordsRejected(t *testing.T) {
	err := Validate("Redact Mod", []string{}, []string{"Redact Mod"})
	if !errors.Is(err, ErrNotOneWord) {
		t.Fatalf("expected ErrNotOneWord, got %v", err)
	}
}

func TestValidateLowercaseRejected(t *testing.T) {
	err := Validate("redact", []string{}, []string{"redact"})
	if !errors.Is(err, ErrNotCapitalized) {
		t.Fatalf("expected ErrNotCapitalized, got %v", err)
	}
}

func TestValidateDuplicateRejected(t *testing.T) {
	err := Validate("Accept", []string{"Accept"}, []string{"Accept"})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
}

func TestValidateMissingMapRowRejected(t *testing.T) {
	err := Validate("Shapes", []string{}, []string{})
	if !errors.Is(err, ErrNoMapRow) {
		t.Fatalf("expected ErrNoMapRow, got %v", err)
	}
}
