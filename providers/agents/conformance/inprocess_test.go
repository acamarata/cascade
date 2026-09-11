// Purpose: runs the full nineteen-case Suite IN-PROCESS against
//
//	conformingProvider (conforming_provider_test.go), which no other test
//	in this package does: TestSuiteSkipsWithNilFactory proves the skip
//	guard, TestSuiteFailsAgainstEmptyImplementation proves non-vacuity via
//	a re-exec'd CHILD process whose coverage counters are never attributed
//	to this package's own `go test -cover` profile, and a driver ticket's
//	own _test.go wires a real driver later. This file closes that gap
//	without touching or weakening the child-process proof: both proofs
//	stay, each for what only it can show — the child proves the suite
//	rejects an empty implementation, this file proves the SAME cases, run
//	in this same process, accept and are exercised by a real one.
//
// SPORT: providers.agents.conformance/ADD (P1-E30-W6-S61-T1).
package conformance

import (
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// TestSuiteRunsInProcessAgainstConformingImplementation drives every one
// of the nineteen named cases (the same set TestConformanceCaseInventory
// enumerates) against conformingProvider in this process and fails loudly
// if any of them fails: a regression here means either the double drifted
// from the contract or a case started asserting something the double does
// not honor, both worth surfacing immediately.
func TestSuiteRunsInProcessAgainstConformingImplementation(t *testing.T) {
	s := &Suite{New: func() (provider.AgentProvider, error) { return newConformingProvider(), nil }}
	cases := map[string]func(*testing.T){
		"TestSpawnHappyPath":                 s.TestSpawnHappyPath,
		"TestSpawnErrEntitlement":            s.TestSpawnErrEntitlement,
		"TestMessageAfterSpawn":              s.TestMessageAfterSpawn,
		"TestStatusReturnsKnownState":        s.TestStatusReturnsKnownState,
		"TestCancelRunning":                  s.TestCancelRunning,
		"TestCancelNonExistent":              s.TestCancelNonExistent,
		"TestCollectBlocksUntilDone":         s.TestCollectBlocksUntilDone,
		"TestCollectBeforeSpawn":             s.TestCollectBeforeSpawn,
		"TestArtifactsEmptyNotNil":           s.TestArtifactsEmptyNotNil,
		"TestNormalizeEventRoundTrip":        s.TestNormalizeEventRoundTrip,
		"TestCancelUncooperativeChild":       s.TestCancelUncooperativeChild,
		"TestSpawnResultCarriesProcessGroup": s.TestSpawnResultCarriesProcessGroup,
		"TestProtocolNegotiationRefusal":     s.TestProtocolNegotiationRefusal,
		"TestEventOrderingAndDedup":          s.TestEventOrderingAndDedup,
		"TestChildEnvAllowlist":              s.TestChildEnvAllowlist,
		"TestPreSpawnSecretScanAborts":       s.TestPreSpawnSecretScanAborts,
		"TestDriverNeverAutoApproves":        s.TestDriverNeverAutoApproves,
		"TestDataClassPropagates":            s.TestDataClassPropagates,
		"TestCredentialCanaryFailsClosed":    s.TestCredentialCanaryFailsClosed,
	}
	if len(cases) != wantCaseCount {
		t.Fatalf("in-process runner covers %d cases, want %d", len(cases), wantCaseCount)
	}
	for name, fn := range cases {
		if !t.Run(name, fn) {
			t.Errorf("%s failed against the conforming in-process implementation", name)
		}
	}
}

// TestSuiteHandlesUnentitledProvider exercises the entitlement REFUSAL
// branch end to end, the complement of the acceptance branch
// TestSuiteRunsInProcessAgainstConformingImplementation already covers:
// TestSpawnErrEntitlement must still pass against a provider that honestly
// declares no programmatic entitlement, exercising the ErrEntitlement
// comparison rather than leaving it dead code no correct provider ever
// reaches.
func TestSuiteHandlesUnentitledProvider(t *testing.T) {
	s := &Suite{New: func() (provider.AgentProvider, error) { return newConformingProviderNotEntitled(), nil }}
	if !t.Run("TestSpawnErrEntitlement", s.TestSpawnErrEntitlement) {
		t.Error("TestSpawnErrEntitlement failed against an honestly unentitled provider")
	}
}

// TestSuiteHandlesUncooperativeCancel exercises the ErrCancelUnconfirmed
// branch of the cancel-ladder cases with a provider that honestly reports
// it instead of confirming exit, and confirms both cases still pass: an
// unconfirmed cancel is not the same defect as a job silently left
// running.
func TestSuiteHandlesUncooperativeCancel(t *testing.T) {
	s := &Suite{New: func() (provider.AgentProvider, error) { return newConformingProviderUncooperativeCancel(), nil }}
	if !t.Run("TestCancelRunning", s.TestCancelRunning) {
		t.Error("TestCancelRunning failed against an honestly uncooperative provider")
	}
	if !t.Run("TestCancelUncooperativeChild", s.TestCancelUncooperativeChild) {
		t.Error("TestCancelUncooperativeChild failed against an honestly uncooperative provider")
	}
}

// TestSuiteHandlesPendingApproval exercises TestDriverNeverAutoApproves's
// received-request branch against a provider with one genuine
// ApprovalRequest already queued, rather than only the empty-window branch
// every other run in this file exercises.
func TestSuiteHandlesPendingApproval(t *testing.T) {
	s := &Suite{New: func() (provider.AgentProvider, error) { return newConformingProviderWithPendingApproval(), nil }}
	if !t.Run("TestDriverNeverAutoApproves", s.TestDriverNeverAutoApproves) {
		t.Error("TestDriverNeverAutoApproves failed against a provider with a genuine queued approval")
	}
}
