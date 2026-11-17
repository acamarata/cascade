package mod

import (
	"errors"
	"testing"
)

func TestCheckBinaryDepsHookClean(t *testing.T) {
	deps := map[string][]string{
		"capp-hook": {"os", "fmt"},
	}
	if err := CheckBinaryDeps(deps); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestCheckBinaryDepsHookNetHTTPRejected(t *testing.T) {
	deps := map[string][]string{
		"capp-hook": {"os", "net/http"},
	}
	err := CheckBinaryDeps(deps)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var forbidden *ErrForbiddenDep
	if !errors.As(err, &forbidden) {
		t.Fatalf("expected ErrForbiddenDep, got %T: %v", err, err)
	}
	if forbidden.Pkg != "net/http" {
		t.Fatalf("expected pkg net/http, got %s", forbidden.Pkg)
	}
}

func TestCheckBinaryDepsDecideAllowsNetHTTP(t *testing.T) {
	deps := map[string][]string{
		"capp-hook":   {"os"},
		"capp-decide": {"net/http"},
	}
	if err := CheckBinaryDeps(deps); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestCheckBinaryDepsMissingHookFails(t *testing.T) {
	deps := map[string][]string{
		"capp-decide": {"net/http"},
	}
	err := CheckBinaryDeps(deps)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var missing *ErrMissingBinary
	if !errors.As(err, &missing) {
		t.Fatalf("expected ErrMissingBinary, got %T: %v", err, err)
	}
	if missing.Binary != "capp-hook" {
		t.Fatalf("expected binary capp-hook, got %s", missing.Binary)
	}
}
