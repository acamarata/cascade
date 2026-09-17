package coretools

// Purpose (this file): the rule that keeps an elevated verb off the MCP
//   surface — 07-CLI-COMMAND-TREE's header rule that elevated verbs are
//   CLI + RPC-with-auth only, and never MCP tools.
//
// WHY IT IS ASSERTED HERE AND NOW. Nothing registered today violates it,
//   and that is exactly when a rule like this is worth writing down: the
//   sync ✦ pair (sync.status, sync.conflicts_list) is due to ride the
//   generated mapping, and `sync.conflicts_resolve` sits next to them in
//   the same noun. A model that could discard the server's copy of a
//   config record on its own reasoning is a surface nobody asked for.
// SPORT: internal/mcp/coretools elevated exclusion (ADD tests) —
//   P1-E17-W4-S38-T3.

import (
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
)

// TestNoRegisteredToolIsAnElevatedVerb walks every tool this package
// registers and refuses any whose RPC method appears in the elevation
// table — conditionally elevated counts, because MCP carries no
// attestation, so a conditionally-gated verb reached this way would
// arrive with no gate in front of it at all.
func TestNoRegisteredToolIsAnElevatedVerb(t *testing.T) {
	specs := Specs()
	if len(specs) == 0 {
		t.Fatal("no tool specs; this test would pass having checked nothing")
	}
	for _, spec := range specs {
		if rpc.IsElevatedVerb(spec.Method) {
			t.Errorf("MCP tool %q is bound to %q, which is an elevated verb: elevated verbs are "+
				"CLI and RPC-with-auth only, and MCP carries no attestation", spec.Name, spec.Method)
		}
	}
}

// TestTheExclusionWouldCatchAnElevatedVerb is the mutation proof: the
// check above passes today because nothing violates it, which is
// indistinguishable from a check that cannot fail unless the predicate is
// exercised against a verb that does.
func TestTheExclusionWouldCatchAnElevatedVerb(t *testing.T) {
	if !rpc.IsElevatedVerb("sync.conflicts_resolve") {
		t.Error("sync.conflicts_resolve is not in the elevation table, so the exclusion above " +
			"would not catch it being registered as a tool")
	}
	if rpc.IsElevatedVerb("context.slice") {
		t.Error("context.slice reports as elevated; the predicate answers true for everything")
	}
}

// TestAnElevatedSpecIsNotExposable is the mutation proof for the skip
// inside Registrations. Nothing in Specs() is bound to an elevated verb
// today, so the branch is never taken over the real set; this drives it
// directly with a spec that is.
func TestAnElevatedSpecIsNotExposable(t *testing.T) {
	elevated := Spec{Name: "cascade_sync_conflicts_resolve", Method: "sync.conflicts_resolve"}
	if exposable(elevated) {
		t.Error("a tool bound to sync.conflicts_resolve would be registered; MCP carries no " +
			"attestation, so it would reach the handler with no gate in front of it")
	}
	ordinary := Spec{Name: "cascade_context_slice", Method: "context.slice"}
	if !exposable(ordinary) {
		t.Error("an ordinary read verb is not exposable; the predicate refuses everything")
	}
}
