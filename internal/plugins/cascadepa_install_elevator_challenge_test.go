package plugins

// Purpose (this file): unit coverage for issueInstallChallenge's own error
//   branches (a real rpc.ElevationMiddleware gate never produces most of
//   these shapes -- they are this function's OWN defensive parsing of
//   whatever *rpc.ErrorObject a gate returns) and Elevate's path-resolution
//   failure, none of which cascadepa_install_elevator_test.go's real,
//   end-to-end keystore/trust-store runs reach. Round-2 rework, T0
//   decision D3 (coverage).
// SPORT: internal/plugins:cascadepa-install-wiring (TEST) -- FIX P1-E24-W5-S50-T4 (D3).

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestInstallElevator_PathResolutionFailurePropagates drives Elevate's own
// resolvePaths error branch -- every other elevator test uses a working
// tempDataPathProvider, so this branch is otherwise never reached.
func TestInstallElevator_PathResolutionFailurePropagates(t *testing.T) {
	wantErr := errors.New("boom: no home directory")
	e := newInstallElevator(func() (runtime.PathProvider, error) { return nil, wantErr },
		testkit.NewFrozenClock(fixedInstallTestTime), func(string) string { return "" })
	_, err := e.Elevate(context.Background(), testElevationRequest())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Elevate: err = %v, want it to wrap %v", err, wantErr)
	}
}

// TestIssueInstallChallenge_GateSucceedsWithNoChallengeRefuses drives the
// "gate returned no error at all" branch -- a real gate only does this
// when the method/params were not classified as elevated, which
// installElevationArgs' own header explains never happens for this file's
// real call, so this is issueInstallChallenge's own defensive case,
// exercised directly with a trivial always-succeeds gate.
func TestIssueInstallChallenge_GateSucceedsWithNoChallengeRefuses(t *testing.T) {
	gate := func(context.Context, json.RawMessage) (any, error) { return "approved", nil }
	_, err := issueInstallChallenge(context.Background(), gate, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("issueInstallChallenge: err = nil, want a refusal when the gate issued no challenge at all")
	}
}

// TestIssueInstallChallenge_GateReturnsAnUnrelatedErrorPropagates drives
// the "err is non-nil but not an ELEVATION_REQUIRED *rpc.ErrorObject"
// branch.
func TestIssueInstallChallenge_GateReturnsAnUnrelatedErrorPropagates(t *testing.T) {
	wantErr := errors.New("boom: gate exploded")
	gate := func(context.Context, json.RawMessage) (any, error) { return nil, wantErr }
	_, err := issueInstallChallenge(context.Background(), gate, json.RawMessage(`{}`))
	if !errors.Is(err, wantErr) {
		t.Fatalf("issueInstallChallenge: err = %v, want it to wrap %v", err, wantErr)
	}
}

// TestIssueInstallChallenge_UnmarshalableChallengeDataRefuses drives the
// json.Marshal(rpcErr.Data) failure branch -- a real ElevationMiddleware
// challenge's Data is always a plain nonce map, so this shape (a channel,
// which json can never encode) only occurs through a fabricated
// *rpc.ErrorObject.
func TestIssueInstallChallenge_UnmarshalableChallengeDataRefuses(t *testing.T) {
	gate := func(context.Context, json.RawMessage) (any, error) {
		return nil, &rpc.ErrorObject{Code: cascade.RPCCodeElevationRequired, Data: make(chan int)}
	}
	_, err := issueInstallChallenge(context.Background(), gate, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("issueInstallChallenge: err = nil, want a refusal when the challenge Data cannot be re-encoded")
	}
}

// TestIssueInstallChallenge_ChallengeWithNoNonceRefuses drives the
// "decoded challenge carries an empty nonce" branch.
func TestIssueInstallChallenge_ChallengeWithNoNonceRefuses(t *testing.T) {
	gate := func(context.Context, json.RawMessage) (any, error) {
		return nil, &rpc.ErrorObject{Code: cascade.RPCCodeElevationRequired, Data: map[string]string{"unrelated": "field"}}
	}
	_, err := issueInstallChallenge(context.Background(), gate, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("issueInstallChallenge: err = nil, want a refusal when the challenge names no nonce")
	}
}
