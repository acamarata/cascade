package docket

import (
	"errors"
	"fmt"
)

// SplitParent represents the original ticket being split.
type SplitParent struct {
	// Acceptance holds the acceptance criteria lines of the parent ticket.
	Acceptance []string
	// Scope holds the scope items of the parent ticket.
	Scope []string
}

// Child represents one of the tickets resulting from a split.
type Child struct {
	// Acceptance holds the acceptance criteria lines mapped to this child.
	Acceptance []string
	// Scope holds the scope items assigned to this child.
	Scope []string
}

// ErrUnmappedAcceptance is returned when a parent acceptance line has no child mapping.
var ErrUnmappedAcceptance = errors.New("unmapped acceptance line")

// ErrScopeMismatch is returned when the union of child scopes does not exactly equal the parent scope.
var ErrScopeMismatch = errors.New("scope union mismatch")

// ValidateSplit gates ticket splits by enforcing acceptance mapping and scope union rules.
// Every parent acceptance line must map to at least one child, and the union of child scopes
// must exactly equal the parent scope. Invalid splits return typed rejection errors carrying
// D2466 for unmapped acceptance lines and D2467 for scope union mismatches.
func ValidateSplit(parent SplitParent, children []Child) error {
	childAcceptance := make(map[string]bool)
	for _, c := range children {
		for _, a := range c.Acceptance {
			childAcceptance[a] = true
		}
	}

	for _, a := range parent.Acceptance {
		if !childAcceptance[a] {
			return fmt.Errorf("D2466: acceptance %q not mapped to any child: %w", a, ErrUnmappedAcceptance)
		}
	}

	parentScope := make(map[string]bool)
	for _, s := range parent.Scope {
		parentScope[s] = true
	}

	childScopeUnion := make(map[string]bool)
	for _, c := range children {
		for _, s := range c.Scope {
			childScopeUnion[s] = true
		}
	}

	for s := range childScopeUnion {
		if !parentScope[s] {
			return fmt.Errorf("D2467: child scope %q outside parent scope: %w", s, ErrScopeMismatch)
		}
	}

	for s := range parentScope {
		if !childScopeUnion[s] {
			return fmt.Errorf("D2467: parent scope %q missing from children: %w", s, ErrScopeMismatch)
		}
	}

	return nil
}
