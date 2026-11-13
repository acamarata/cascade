package background

import (
	"errors"
	"fmt"
	"testing"
)

// TestMissingScopeWraps pins the sentinel a later brief slice returns.
func TestMissingScopeWraps(t *testing.T) {
	if !errors.Is(fmt.Errorf("brief: %w", ErrMissingScope), ErrMissingScope) {
		t.Fatal("ErrMissingScope is lost through %w wrapping")
	}
}
