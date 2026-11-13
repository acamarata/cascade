package background

import (
	"errors"
	"testing"
)

// TestNonAutonomyReturnsNeedsUser verifies that ResolveQuestion returns
// ErrNeedsUser for unresolved questions when not in full autonomy mode.
func TestNonAutonomyReturnsNeedsUser(t *testing.T) {
	modes := []string{"normal", "default", "auto", ""}
	cases := []struct {
		question string
		def      string
	}{
		{"what is the meaning?", "42"},
		{"what is the meaning?", ""},
		{"another question?", "default"},
	}

	for _, mode := range modes {
		for _, q := range cases {
			t.Run(mode+"_"+q.question, func(t *testing.T) {
				r := NewResolver(mode, nil)
				val, err := r.ResolveQuestion(q.question, q.def)
				if val != "" {
					t.Errorf("expected empty string, got %q", val)
				}
				if !errors.Is(err, ErrNeedsUser) {
					t.Errorf("expected ErrNeedsUser, got %v", err)
				}
			})
		}
	}
}
