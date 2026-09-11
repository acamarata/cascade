// Purpose: compile-only checks over the Round-35 lifecycle cases: a
//
//	nil-factory guard proving each skips cleanly before any driver is
//	wired. TestSuiteFailsAgainstEmptyImplementation in suite_test.go
//	carries the shared non-vacuity proof for the whole Suite.
//
// SPORT: providers.agents.conformance/ADD (P1-E30-W6-S61-T1).
package conformance

import "testing"

func TestLifecycleCasesSkipWithNilFactory(t *testing.T) {
	s := &Suite{}
	names := map[string]func(*testing.T){
		"TestCancelUncooperativeChild":       s.TestCancelUncooperativeChild,
		"TestSpawnResultCarriesProcessGroup": s.TestSpawnResultCarriesProcessGroup,
		"TestProtocolNegotiationRefusal":     s.TestProtocolNegotiationRefusal,
	}
	for name, fn := range names {
		if ok := t.Run(name, fn); !ok {
			t.Errorf("%s reported failure with a nil factory, want a clean skip", name)
		}
	}
	// TestEventOrderingAndDedup does not call s.newProvider — it exercises
	// provider.EventNormalizer directly — so it runs (not skips) even
	// with a nil factory.
	if !t.Run("TestEventOrderingAndDedup", s.TestEventOrderingAndDedup) {
		t.Error("TestEventOrderingAndDedup failed with a nil factory, want pass (it needs no provider)")
	}
}
