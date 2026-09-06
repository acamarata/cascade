// Purpose: unit tests for `cascade recall index rebuild|verify|migrate|
// update` — root-command reachability and the flag-to-RPC-method mapping
// through an injected call seam. The full composition-root wiring proof
// (a real daemon.RegisterRecallIndexHandler call inside the REAL
// buildRPCServer, over a real socket) is
// cmd/cascade/recall_index_integration_test.go.
//
// SPORT: cmd.cascade.cmd.recall.index (ADD, P1-E06-W2-S11-T4).
package main

import (
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
)

// TestRecallIndexResolvesOnTheRealRootCommand is the reachability proof
// for all four verbs, following recall_test.go's exact pattern.
func TestRecallIndexResolvesOnTheRealRootCommand(t *testing.T) {
	for _, verb := range []string{"rebuild", "verify", "migrate", "update"} {
		t.Run(verb, func(t *testing.T) {
			cmd, _, err := newRootCmd().Find([]string{"recall", "index", verb})
			if err != nil {
				t.Fatalf("recall index %s did not resolve on the real root: %v", verb, err)
			}
			if cmd.Name() != verb {
				t.Fatalf("resolved %q, want %s", cmd.Name(), verb)
			}
			if cmd.RunE == nil {
				t.Fatalf("recall index %s resolved but has no RunE", verb)
			}
		})
	}
}

// TestRecallIndexVerbsCallTheirMethod proves each verb calls its own
// recall.index.* method name with no arguments, through the same
// recallHarness recall_test.go's cases use.
func TestRecallIndexVerbsCallTheirMethod(t *testing.T) {
	cases := map[string]string{
		"rebuild": daemon.RecallIndexRebuildMethod,
		"verify":  daemon.RecallIndexVerifyMethod,
		"migrate": daemon.RecallIndexMigrateMethod,
		"update":  daemon.RecallIndexUpdateMethod,
	}
	for verb, method := range cases {
		t.Run(verb, func(t *testing.T) {
			h := &recallHarness{result: map[string]any{}}
			if _, _, err := h.run(t, "index", verb); err != nil {
				t.Fatalf("recall index %s: %v", verb, err)
			}
			if len(h.calls) != 1 || h.calls[0].Method != method {
				t.Fatalf("calls = %+v, want one %s", h.calls, method)
			}
		})
	}
}

// TestRecallIndexVerbsTakeNoArgs proves each verb refuses a positional
// argument (Args: usageArgs(cobra.NoArgs)).
func TestRecallIndexVerbsTakeNoArgs(t *testing.T) {
	for _, verb := range []string{"rebuild", "verify", "migrate", "update"} {
		t.Run(verb, func(t *testing.T) {
			h := &recallHarness{}
			if _, _, err := h.run(t, "index", verb, "unexpected"); err == nil {
				t.Fatalf("recall index %s accepted an unexpected argument", verb)
			}
		})
	}
}
