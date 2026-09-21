// Purpose (this file): lands the RPC dispatch test the confirming review
//
//	(2026-09-21, P1-E25-W5-S52-T5 FIX-THEN-SHIP D3) requires as a permanent
//	fixture, replacing the fix lane's admitted "no daemon harness" gap --
//	no daemon is needed: a plain rpc.NewRegistry() plus registry.Register(reviewRPCMethod, reviewRPCHandler)
//	(review_mount.go) is the full seam under test.
//
// Inputs: none beyond the process-wide plugins/review.reviewProvider var,
//
//	swapped via SetReviewProvider around each dispatch and always restored
//	by t.Cleanup (the same discipline review_mount_test.go already uses)
//	-- never left holding a test double for a later test in this binary.
//
// Outputs: proves reviewRPCMethod ("plugin.review.review"), once bound on
//
//	a real *rpc.Registry, dispatches to the SAME reviewplugin.Review seam
//	the mounted CLI command reads (happy path), and that malformed JSON
//	params, empty params, and an invalid level each refuse with a wire
//	code that maps back to cascade.KindInvalidInput via
//	cascade.KindFromJSONRPCCode -- never a panic or a silent zero-value
//	dispatch to the provider.
//
// Constraints: no network, no daemon process (Art.7); reuses
//
//	mountedFindingProvider and mountedRefusingProvider from
//	review_mount_test.go rather than declaring a second pair of fakes for
//	the same contract (DRY, ASI Policy 3).
//
// SPORT: cmd/cascade:review-rpc-test (ADD) -- FIX P1-E25-W5-S52-T5 (D3).
package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	reviewplugin "github.com/acamarata/cascade/plugins/review"
)

// newReviewRPCRegistry builds a real *rpc.Registry with only
// the same Register call daemon_unix_run.go's
// buildRPCServer makes at daemon startup, minus every other method this
// test does not exercise.
func newReviewRPCRegistry() *rpc.Registry {
	registry := rpc.NewRegistry()
	registry.Register(reviewRPCMethod, reviewRPCHandler)
	return registry
}

// TestReviewRPCDispatch_HappyPath proves plugin.review.review, dispatched
// through a real Registry, reaches the exact reviewplugin.Review seam the
// mounted `cascade review` command reads -- the fake provider's finding
// comes back in the result envelope unchanged.
func TestReviewRPCDispatch_HappyPath(t *testing.T) {
	t.Cleanup(func() { _ = reviewplugin.SetReviewProvider(mountedRefusingProvider{}) })
	if err := reviewplugin.SetReviewProvider(mountedFindingProvider{}); err != nil {
		t.Fatalf("SetReviewProvider(mountedFindingProvider): %v", err)
	}

	registry := newReviewRPCRegistry()
	params, err := json.Marshal(reviewRPCParams{Level: string(provider.ReviewCRLevelB), Diff: "diff --git a/x b/x\n"})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	result, errObj := registry.Dispatch(context.Background(), &rpc.Request{
		Method: reviewRPCMethod,
		Params: params,
	})
	if errObj != nil {
		t.Fatalf("Dispatch error: code=%d message=%s", errObj.Code, errObj.Message)
	}
	resp, ok := result.(provider.ReviewResponse)
	if !ok {
		t.Fatalf("result type = %T, want provider.ReviewResponse", result)
	}
	if len(resp.Findings) != 1 || resp.Findings[0].Message != "mounted-fixture finding" {
		t.Fatalf("Findings = %+v, want the fake provider's single finding", resp.Findings)
	}
}

// TestReviewRPCDispatch_InvalidInput proves every malformed-input path
// reviewRPCHandler refuses on (empty params, malformed JSON, an invalid
// level) maps back to cascade.KindInvalidInput via
// cascade.KindFromJSONRPCCode -- never a panic, never a zero-value
// dispatch to the provider.
func TestReviewRPCDispatch_InvalidInput(t *testing.T) {
	t.Cleanup(func() { _ = reviewplugin.SetReviewProvider(mountedRefusingProvider{}) })
	if err := reviewplugin.SetReviewProvider(mountedFindingProvider{}); err != nil {
		t.Fatalf("SetReviewProvider(mountedFindingProvider): %v", err)
	}

	registry := newReviewRPCRegistry()

	cases := []struct {
		name   string
		params json.RawMessage
	}{
		{name: "empty params", params: nil},
		{name: "malformed JSON", params: json.RawMessage(`{not json`)},
		{name: "invalid level", params: json.RawMessage(`{"level":"A","diff":"x"}`)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, errObj := registry.Dispatch(context.Background(), &rpc.Request{
				Method: reviewRPCMethod,
				Params: tc.params,
			})
			if errObj == nil {
				t.Fatalf("Dispatch returned no error, result = %+v, want a KindInvalidInput refusal", result)
			}
			kind, ok := cascade.KindFromJSONRPCCode(errObj.Code)
			if !ok || kind != cascade.KindInvalidInput {
				t.Fatalf("error code = %d -> kind=%v ok=%v, want KindInvalidInput", errObj.Code, kind, ok)
			}
		})
	}
}
