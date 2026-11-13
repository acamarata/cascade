package write

import (
	"errors"
	"testing"
)

func TestNoVerifyNodesForbiddenOnRestart(t *testing.T) {
	cfg := RunConfig{
		Context:       "restart",
		NoVerifyNodes: true,
	}
	err := ValidateRunConfig(cfg)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrNoVerifyNodesForbidden) {
		t.Fatalf("expected error M1098, got %v", err)
	}
}

func TestNoVerifyNodesForbiddenOnTuning(t *testing.T) {
	cfg := RunConfig{
		Context:       "tuning",
		NoVerifyNodes: true,
	}
	err := ValidateRunConfig(cfg)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrNoVerifyNodesForbidden) {
		t.Fatalf("expected error M1098, got %v", err)
	}
}

func TestNoVerifyNodesAllowedOnStart(t *testing.T) {
	cfg := RunConfig{
		Context:       "start",
		NoVerifyNodes: true,
	}
	err := ValidateRunConfig(cfg)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestNoVerifyNodesFalseOnRestartPasses(t *testing.T) {
	cfg := RunConfig{
		Context:       "restart",
		NoVerifyNodes: false,
	}
	err := ValidateRunConfig(cfg)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
