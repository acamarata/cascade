package docket

import (
	"errors"
	"testing"
)

func TestValidResourcesPass(t *testing.T) {
	resources := Resources{"tool:go", "node:worker_1", "fixture:small", "cred:github_read"}
	lane := LaneCaps{CredClasses: []string{"github_read"}}

	err := ValidateResources(resources, lane)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestEmptyResourcesPass(t *testing.T) {
	// nil resources, nil caps
	err := ValidateResources(nil, LaneCaps{CredClasses: nil})
	if err != nil {
		t.Fatalf("expected nil for nil resources, got %v", err)
	}

	// empty resources, empty caps
	err = ValidateResources(Resources{}, LaneCaps{CredClasses: []string{}})
	if err != nil {
		t.Fatalf("expected nil for empty resources, got %v", err)
	}

	// invalid caps should still fail
	err = ValidateResources(nil, LaneCaps{CredClasses: []string{"INVALID"}})
	if !errors.Is(err, ErrInvalidResources) {
		t.Fatalf("expected ErrInvalidResources for invalid caps, got %v", err)
	}
}
