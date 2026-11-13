package docket

import (
	"errors"
	"testing"
)

// TestCredentialClassNotInLaneReturnsD4555 verifies that a credential resource
// not present in lane.CredClasses returns ErrInvalidResources.
// It covers nil caps, empty caps, non-matching caps, and differently cased names.
func TestCredentialClassNotInLaneReturnsD4555(t *testing.T) {
	credResource := Resources{"cred:github_read"}

	t.Run("nil caps", func(t *testing.T) {
		lane := LaneCaps{CredClasses: nil}
		err := ValidateResources(credResource, lane)
		if !errors.Is(err, ErrInvalidResources) {
			t.Fatalf("expected ErrInvalidResources, got %v", err)
		}
	})

	t.Run("empty caps", func(t *testing.T) {
		lane := LaneCaps{CredClasses: []string{}}
		err := ValidateResources(credResource, lane)
		if !errors.Is(err, ErrInvalidResources) {
			t.Fatalf("expected ErrInvalidResources, got %v", err)
		}
	})

	t.Run("nonmatching caps", func(t *testing.T) {
		lane := LaneCaps{CredClasses: []string{"github_write", "gitlab_read"}}
		err := ValidateResources(credResource, lane)
		if !errors.Is(err, ErrInvalidResources) {
			t.Fatalf("expected ErrInvalidResources, got %v", err)
		}
	})

	t.Run("differently cased cred", func(t *testing.T) {
		lane := LaneCaps{CredClasses: []string{"github_read"}}
		upperCred := Resources{"cred:GitHub_Read"}
		err := ValidateResources(upperCred, lane)
		if !errors.Is(err, ErrInvalidResources) {
			t.Fatalf("expected ErrInvalidResources for differently cased cred, got %v", err)
		}
	})

	t.Run("differently cased cap", func(t *testing.T) {
		lane := LaneCaps{CredClasses: []string{"GitHub_Read"}}
		err := ValidateResources(credResource, lane)
		if !errors.Is(err, ErrInvalidResources) {
			t.Fatalf("expected ErrInvalidResources for differently cased cap, got %v", err)
		}
	})

	t.Run("duplicate caps", func(t *testing.T) {
		lane := LaneCaps{CredClasses: []string{"github_read", "github_read"}}
		err := ValidateResources(credResource, lane)
		if !errors.Is(err, ErrInvalidResources) {
			t.Fatalf("expected ErrInvalidResources for duplicate caps, got %v", err)
		}
	})

	t.Run("wildcard in caps", func(t *testing.T) {
		lane := LaneCaps{CredClasses: []string{"*"}}
		err := ValidateResources(credResource, lane)
		if !errors.Is(err, ErrInvalidResources) {
			t.Fatalf("expected ErrInvalidResources for wildcard cap, got %v", err)
		}
	})

	t.Run("invalid cap chars", func(t *testing.T) {
		lane := LaneCaps{CredClasses: []string{"github.read"}}
		err := ValidateResources(credResource, lane)
		if !errors.Is(err, ErrInvalidResources) {
			t.Fatalf("expected ErrInvalidResources for invalid cap chars, got %v", err)
		}
	})

	t.Run("cap starts with number", func(t *testing.T) {
		lane := LaneCaps{CredClasses: []string{"1github"}}
		err := ValidateResources(credResource, lane)
		if !errors.Is(err, ErrInvalidResources) {
			t.Fatalf("expected ErrInvalidResources for cap starting with number, got %v", err)
		}
	})

	t.Run("matching case passes", func(t *testing.T) {
		lane := LaneCaps{CredClasses: []string{"github_read"}}
		err := ValidateResources(credResource, lane)
		if err != nil {
			t.Fatalf("expected nil error for matching cred, got %v", err)
		}
	})
}
