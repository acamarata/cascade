package mod

import (
	"errors"
	"strings"
	"testing"
)

func TestLegacyCIClean(t *testing.T) {
	err := CheckLegacyCI([]string{"internal/mod/a.go"}, "core/ci/")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestLegacyCIRejectsChange(t *testing.T) {
	err := CheckLegacyCI([]string{"core/ci/run.go"}, "core/ci/")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrLegacyFrozen) {
		t.Fatalf("expected error to wrap ErrLegacyFrozen, got %v", err)
	}
}

func TestLegacyCIListsPaths(t *testing.T) {
	err := CheckLegacyCI([]string{"core/ci/run.go"}, "core/ci/")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "core/ci/run.go") {
		t.Fatalf("expected error message to contain 'core/ci/run.go', got %v", err.Error())
	}
}

func TestLegacyCIEmptyPasses(t *testing.T) {
	err := CheckLegacyCI([]string{}, "core/ci/")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
