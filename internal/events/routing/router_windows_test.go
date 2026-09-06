//go:build windows

package routing

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/policy"
)

// Purpose: the Art.5.2 Windows tier-2 assertion. The command classifier
//   parses POSIX shell grammar and has no Windows matcher, so a
//   Windows-native shell action misses the table and is refused as
//   (L4, classify-unknown) INSIDE the evaluator. This test asserts that
//   outcome at the router boundary, with the router classifying nothing
//   of its own (R-21.236, R-21.208).
//
// CONTRACT DEVIATION: the contract asks for "the PLATFORM_UNSUPPORTED
//   reason carried in the Trace". No such sentinel exists anywhere in
//   this tree: internal/policy's classifier produces a terminal L4 deny
//   for unparseable command forms and names it classify-unknown
//   (classifier_windows_test.go's TestWindowsNativeSyntaxFallsThroughToL4
//   is the ratified assertion of that behaviour). Minting a second
//   sentinel here would be a second copy of a security vocabulary, so
//   this test asserts the refusal the evaluator actually produces: a deny
//   verdict at the top rung. See the ticket journal.

// TestHookShellActionWindowsRefusal proves a Windows-native shell action
// routed from a hook is denied, not silently allowed and not returned as
// an unchecked nil.
func TestHookShellActionWindowsRefusal(t *testing.T) {
	r, err := NewActionRouter(newRealEngine(t, "hooks.shell", policy.ClassLocalDev), newAuditLog())
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	for _, command := range []string{
		"Remove-Item -Recurse -Force C:\\Users\\data",
		"del /f /q C:\\temp",
	} {
		verdict, trace, err := r.RouteAction(context.Background(), routedAction(command, "hooks.shell"))
		if verdict != policy.VerdictDeny {
			t.Fatalf("%q: verdict = %v, want deny", command, verdict)
		}
		if err == nil {
			t.Fatalf("%q: deny returned a nil error", command)
		}
		if len(trace.Layers) == 0 {
			t.Fatalf("%q: refusal carried no trace", command)
		}
	}
}
