package log

import (
	"errors"
	"fmt"
	"testing"
)

// TestSentinelsWrapAndStayDistinct pins the sentinel errors a later slice returns: each is set, survives %w wrapping, and differs
// from the others, so errors.Is at the caller keeps telling them apart.
func TestSentinelsWrapAndStayDistinct(t *testing.T) {
	sentinels := []error{
		ErrInvalidRate,
	}
	for i, s := range sentinels {
		if s == nil || s.Error() == "" {
			t.Fatalf("sentinel %d is nil or has no text", i)
		}
		if !errors.Is(fmt.Errorf("wrapped: %w", s), s) {
			t.Fatalf("sentinel %q is lost through %%w wrapping", s)
		}
		for j, o := range sentinels {
			if i != j && errors.Is(s, o) {
				t.Fatalf("sentinels %q and %q match each other", s, o)
			}
		}
	}
}
