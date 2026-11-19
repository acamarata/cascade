package background

import (
	"testing"
)

func TestEmptyDeniedActionsAllowsAll(t *testing.T) {
	// [] parses to nonnil empty slice and allows "write"
	cfg, err := ParseConfig([]byte(`{"denied_actions":[]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DeniedActions == nil {
		t.Fatal("expected non-nil empty slice for []")
	}
	if len(cfg.DeniedActions) != 0 {
		t.Fatalf("expected empty slice, got %v", cfg.DeniedActions)
	}
	if err := cfg.Check("write"); err != nil {
		t.Fatalf("expected 'write' to be allowed, got %v", err)
	}

	// null field parses to nil slice and allows nonblank actions
	cfg2, err := ParseConfig([]byte(`{"denied_actions":null}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg2.DeniedActions != nil {
		t.Fatal("expected nil slice for null")
	}
	if err := cfg2.Check("some_action"); err != nil {
		t.Fatalf("expected nonblank actions to be allowed, got %v", err)
	}
}

func TestMissingDeniedActionsFieldDefaultsEmpty(t *testing.T) {
	// {} gives nil DeniedActions and allows "exec"
	cfg, err := ParseConfig([]byte(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DeniedActions != nil {
		t.Fatal("expected nil DeniedActions for missing field")
	}
	if err := cfg.Check("exec"); err != nil {
		t.Fatalf("expected 'exec' to be allowed, got %v", err)
	}

	// unknown fields are ignored without changing denied_actions
	cfg2, err := ParseConfig([]byte(`{"unknown_field": "value"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg2.DeniedActions != nil {
		t.Fatal("expected nil DeniedActions when only unknown fields present")
	}
}
