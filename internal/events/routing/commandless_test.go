package routing

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestCommandLessWithCommandRefused pins the contradictory declaration guard.
func TestCommandLessWithCommandRefused(t *testing.T) {
	a := routedAction("cat notes.txt", "hooks.read")
	a.CommandLess = true
	err := a.Validate()
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("Validate = %v, want invalid input", err)
	}
	if !strings.Contains(err.Error(), RouteDeniedCode) {
		t.Fatalf("Validate = %v, want refusal code", err)
	}
}

// TestCommandLessRule pins the shared validation and request rule.
func TestCommandLessRule(t *testing.T) {
	for _, tc := range []struct {
		origin         Origin
		declared, want bool
	}{
		{OriginHook, false, false}, {OriginHook, true, true},
		{OriginScheduler, false, true}, {OriginScheduler, true, true},
	} {
		a := routedAction("", "hooks.read")
		a.Origin, a.CommandLess = tc.origin, tc.declared
		if err := a.Validate(); (err == nil) != tc.want {
			t.Errorf("origin=%s declared=%v: Validate = %v", tc.origin, tc.declared, err)
		}
		if got := a.request().CommandLess; got != tc.want {
			t.Errorf("origin=%s declared=%v: request CommandLess = %v", tc.origin, tc.declared, got)
		}
	}
}
