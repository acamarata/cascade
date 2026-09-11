// Purpose: compile-only checks over the Round-35 security cases: a
//
//	nil-factory guard proving each skips cleanly before any driver is
//	wired, plus a direct run of the two cases that need no provider at
//	all (they exercise pkg-level policy functions).
//
// SPORT: providers.agents.conformance/ADD (P1-E30-W6-S61-T1).
package conformance

import "testing"

func TestSecurityCasesSkipWithNilFactory(t *testing.T) {
	s := &Suite{}
	needsProvider := map[string]func(*testing.T){
		"TestDriverNeverAutoApproves":     s.TestDriverNeverAutoApproves,
		"TestDataClassPropagates":         s.TestDataClassPropagates,
		"TestCredentialCanaryFailsClosed": s.TestCredentialCanaryFailsClosed,
	}
	for name, fn := range needsProvider {
		if ok := t.Run(name, fn); !ok {
			t.Errorf("%s reported failure with a nil factory, want a clean skip", name)
		}
	}

	// These two need no provider at all — they exercise
	// provider.DriverEnvAllowlist/ToolEnvAllowlist and
	// provider.PreSpawnScan directly, so they run and must pass even
	// with a nil factory.
	if !t.Run("TestChildEnvAllowlist", s.TestChildEnvAllowlist) {
		t.Error("TestChildEnvAllowlist failed with a nil factory, want pass")
	}
	if !t.Run("TestPreSpawnSecretScanAborts", s.TestPreSpawnSecretScanAborts) {
		t.Error("TestPreSpawnSecretScanAborts failed with a nil factory, want pass")
	}
}
