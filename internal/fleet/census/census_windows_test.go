//go:build windows

package census

import (
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: pins windows's refusal per-platform (Art.5), mirroring
//
//	providers/sqlite/flock_windows.go's lock_test.go precedent: a
//	build-tagged test asserts the exact refusal so a later edit cannot
//	silently drop or reword it.
//
// SPORT: fleet/census (ADD, per T-1 sport_updates).

func TestEnumerateUnsupportedOnWindows(t *testing.T) {
	c := New(nil)
	_, err := c.Enumerate()
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("err = %v, want ErrUnsupportedPlatform", err)
	}
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("err kind = %v, want unsupported", err)
	}
	if !strings.Contains(err.Error(), unsupportedPlatformMsg) {
		t.Fatalf("err message = %q, want it to contain %q", err.Error(), unsupportedPlatformMsg)
	}
}
