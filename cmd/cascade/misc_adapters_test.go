// Purpose: unit coverage for a handful of small composition-root
//
//	functions that shipped with zero direct test callers:
//	productionStdinIsPiped (vault_quarantine.go), unavailableFanOut
//	(daemon_resume.go), productionElevationPrecondition (daemon.go),
//	clientRecallCall/clientMemoryCall (recall.go/memory.go), and
//	realHTTPDoer.Get's request-construction refusal (provider_usage_cmd.go).
//	Each is exercised directly rather than through the CLI command tree
//	that happens to wire it, since the command tree does not reach every
//	one of these in its own existing suite.
//
// SPORT: cmd.cascade/TEST (composition-root adapter coverage).
package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestProductionStdinIsPiped_Idempotent proves the real os.Stdin.Stat
// check is a pure query: two consecutive calls in the same process, with
// stdin untouched between them, must agree. The test harness's actual
// stdin (tty vs pipe) is not under this test's control, so the concrete
// boolean is not asserted -- but a real Stat-backed implementation is
// deterministic call to call, which this test can and does verify.
func TestProductionStdinIsPiped_Idempotent(t *testing.T) {
	first := productionStdinIsPiped()
	second := productionStdinIsPiped()
	if first != second {
		t.Errorf("productionStdinIsPiped() = %v then %v, want a stable answer across calls", first, second)
	}
}

// TestUnavailableFanOut_RefusesWithKindUnavailable proves the disclosed
// gap this seam documents: until a real conductor.Executor is wired,
// every call refuses with KindUnavailable rather than returning a
// fabricated response.
func TestUnavailableFanOut_RefusesWithKindUnavailable(t *testing.T) {
	_, err := unavailableFanOut(context.Background(), provider.ModelRequest{}, 0, nil, nil, nil)
	if !isCLIKind(err, cascade.KindUnavailable) {
		t.Fatalf("unavailableFanOut() = %v, want KindUnavailable", err)
	}
}

// TestProductionElevationPrecondition_NilPathsFailsClosed proves the
// documented fail-closed default: a nil PathProvider (or one with an
// empty DataDir) reports both preconditions false rather than probing a
// guessed path.
func TestProductionElevationPrecondition_NilPathsFailsClosed(t *testing.T) {
	precondition := productionElevationPrecondition(nil)
	enrolled, available := precondition()
	if enrolled || available {
		t.Errorf("productionElevationPrecondition(nil)() = (%v, %v), want (false, false)", enrolled, available)
	}
}

// TestProductionElevationPrecondition_RealPathsRuns proves the non-nil
// branch actually builds and queries a real elevation.Keystore/
// ElevationTrustStore pair over a fresh data dir, rather than only ever
// taking the nil short-circuit.
func TestProductionElevationPrecondition_RealPathsRuns(t *testing.T) {
	root := t.TempDir()
	paths := fakeDaemonPaths{root: root}
	precondition := productionElevationPrecondition(paths)
	// A fresh, never-enrolled data dir: IsEnrolled must be false. The
	// keystore availability answer is platform-dependent and not
	// asserted here -- only that the real query path runs without error.
	enrolled, _ := precondition()
	if enrolled {
		t.Error("productionElevationPrecondition on a fresh data dir reported enrolled = true, want false")
	}
}

// TestClientRecallCall_TransportUnreachable and
// TestClientMemoryCall_TransportUnreachable prove each dials a real unix
// socket via internal/client (production code) against a path nothing
// listens on -- the same class of proof cascadepa_wiring_test.go's
// TestCascadePAClient_OneShot_TransportUnreachable uses, never a fake
// transport.
func TestClientRecallCall_TransportUnreachable(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "nothing-here.sock")
	var out map[string]any
	err := clientRecallCall(context.Background(), socket, "recall.query", map[string]string{"q": "x"}, &out)
	if err == nil {
		t.Fatal("clientRecallCall against an unreachable socket = nil error, want a transport failure")
	}
}

func TestClientMemoryCall_TransportUnreachable(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "nothing-here.sock")
	var out map[string]any
	err := clientMemoryCall(context.Background(), socket, "memory.soul.show", nil, &out)
	if err == nil {
		t.Fatal("clientMemoryCall against an unreachable socket = nil error, want a transport failure")
	}
}

// TestRealHTTPDoer_Get_InvalidURLRefusesBeforeAnyNetworkIO proves the
// request-construction refusal branch: an unparsable URL fails at
// http.NewRequestWithContext, before any real outbound connection is
// attempted -- the only branch this unit lane can exercise without
// importing net/http itself (internal/build's Art.7.2 gate).
func TestRealHTTPDoer_Get_InvalidURLRefusesBeforeAnyNetworkIO(t *testing.T) {
	doer := realHTTPDoer{}
	_, err := doer.Get(context.Background(), "://not-a-valid-url")
	if err == nil {
		t.Fatal("realHTTPDoer.Get(invalid URL) = nil error, want a request-construction failure")
	}
}
