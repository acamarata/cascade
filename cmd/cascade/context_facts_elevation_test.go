//go:build !windows

// Purpose: context.facts.accept is registered as an always-elevated verb;
//
//	the registration sits in the !windows handler file, so the proof does too.
//
// SPORT: cmd/cascade/context-facts (tests).
package main

import (
	"slices"
	"testing"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/rpc"
)

func TestFactsAcceptRegisteredElevated(t *testing.T) {
	if !rpc.IsElevatedVerb(contextFactsAcceptMethod) {
		t.Fatalf("%s is not an elevated verb", contextFactsAcceptMethod)
	}
	if !rpc.IsElevated(contextFactsAcceptMethod, []byte(`{"id":"fact:r:layout"}`)) {
		t.Fatalf("%s is not always elevated", contextFactsAcceptMethod)
	}
	if !slices.Contains(policy.ElevationClassVerbs(), contextFactsAcceptMethod) {
		t.Fatalf("policy.ElevationClassVerbs() lacks %s", contextFactsAcceptMethod)
	}
	for _, m := range []string{contextFactsListMethod, contextFactsProposeMethod} {
		if rpc.IsElevatedVerb(m) {
			t.Errorf("%s should not be elevated", m)
		}
	}
}
