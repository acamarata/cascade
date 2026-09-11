// Purpose: runIsolated executes one case method against a MISBEHAVING
//
//	provider (misbehaving_provider_test.go) on a *testing.T value of its
//	own, detached from the calling test's pass/fail state exactly as the
//	real testing.T.Run does internally (its own goroutine, its own Goexit
//	boundary): a t.Fatal inside fn unwinds only that goroutine, never the
//	caller's, so the expected failure never fails `go test` itself. The
//	four TestSuiteCatches* functions below assert each one DID fail the
//	case naming the guarantee it breaks.
//
// SPORT: providers.agents.conformance/ADD (P1-E30-W6-S61-T1).
package conformance

import (
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// runIsolated runs fn against a fresh, detached *testing.T and reports
// whether that *testing.T ended up failed. It never touches the caller's
// own *testing.T; only the caller's own check of the returned bool can
// fail the enclosing test.
func runIsolated(fn func(t *testing.T)) bool {
	iso := &testing.T{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(iso)
	}()
	<-done
	return iso.Failed()
}

func TestSuiteCatchesBadSpawnResults(t *testing.T) {
	cases := map[string]func(t *testing.T){
		"empty JobID": func(t *testing.T) {
			s := &Suite{New: func() (provider.AgentProvider, error) { return newSpawnEmptyJobIDProvider(), nil }}
			s.TestSpawnHappyPath(t)
		},
		"negative ProcessGroupID (Spawn)": func(t *testing.T) {
			s := &Suite{New: func() (provider.AgentProvider, error) { return newSpawnNegativePgidProvider(), nil }}
			s.TestSpawnHappyPath(t)
		},
		"negative ProcessGroupID (distinctness)": func(t *testing.T) {
			s := &Suite{New: func() (provider.AgentProvider, error) { return newSpawnNegativePgidProvider(), nil }}
			s.TestSpawnResultCarriesProcessGroup(t)
		},
		"duplicate ProcessGroupID": func(t *testing.T) {
			s := &Suite{New: func() (provider.AgentProvider, error) { return newSpawnDuplicatePgidProvider(), nil }}
			s.TestSpawnResultCarriesProcessGroup(t)
		},
	}
	for name, run := range cases {
		if !runIsolated(run) {
			t.Errorf("%s: suite did not fail against a provider that violates it", name)
		}
	}
}

func TestSuiteCatchesBrokenCancel(t *testing.T) {
	cases := map[string]func(t *testing.T){
		"cancel is a silent no-op (running case)": func(t *testing.T) {
			s := &Suite{New: func() (provider.AgentProvider, error) { return newCancelNoopProvider(), nil }}
			s.TestCancelRunning(t)
		},
		"cancel is a silent no-op (uncooperative case)": func(t *testing.T) {
			s := &Suite{New: func() (provider.AgentProvider, error) { return newCancelNoopProvider(), nil }}
			s.TestCancelUncooperativeChild(t)
		},
		"cancel of a missing job reports no error": func(t *testing.T) {
			s := &Suite{New: func() (provider.AgentProvider, error) { return newCancelNoopProvider(), nil }}
			s.TestCancelNonExistent(t)
		},
	}
	for name, run := range cases {
		if !runIsolated(run) {
			t.Errorf("%s: suite did not fail against a provider that violates it", name)
		}
	}
}

func TestSuiteCatchesBrokenBasics(t *testing.T) {
	cases := map[string]func(t *testing.T){
		"Message never reports a missing job": func(t *testing.T) {
			s := &Suite{New: func() (provider.AgentProvider, error) { return newMessageAlwaysOKProvider(), nil }}
			s.TestMessageAfterSpawn(t)
		},
		"Status returns a non-member state": func(t *testing.T) {
			s := &Suite{New: func() (provider.AgentProvider, error) { return newStatusInvalidProvider(), nil }}
			s.TestStatusReturnsKnownState(t)
		},
		"Collect of an unspawned job reports no error": func(t *testing.T) {
			s := &Suite{New: func() (provider.AgentProvider, error) { return newCollectBeforeSpawnOKProvider(), nil }}
			s.TestCollectBeforeSpawn(t)
		},
		"Artifacts returns a nil slice": func(t *testing.T) {
			s := &Suite{New: func() (provider.AgentProvider, error) { return newArtifactsNilProvider(), nil }}
			s.TestArtifactsEmptyNotNil(t)
		},
	}
	for name, run := range cases {
		if !runIsolated(run) {
			t.Errorf("%s: suite did not fail against a provider that violates it", name)
		}
	}
}

func TestSuiteCatchesBrokenEntitlement(t *testing.T) {
	cases := map[string]func(t *testing.T){
		"entitled provider whose Spawn errors anyway": func(t *testing.T) {
			s := &Suite{New: func() (provider.AgentProvider, error) { return newEntitledButSpawnErrorsProvider(), nil }}
			s.TestSpawnErrEntitlement(t)
		},
		"unentitled provider returning the wrong refusal": func(t *testing.T) {
			s := &Suite{New: func() (provider.AgentProvider, error) { return newWrongEntitlementErrorProvider(), nil }}
			s.TestSpawnErrEntitlement(t)
		},
	}
	for name, run := range cases {
		if !runIsolated(run) {
			t.Errorf("%s: suite did not fail against a provider that violates it", name)
		}
	}
}
