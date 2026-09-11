//go:build integration

// Purpose: the tagged live-lane acceptance test for the lane bench/probe
//   protocol (P1-E12-W3-S25-T5, task 4). Excluded from the default unit
//   lane by the integration build tag - the default unit lane forbids
//   importing net, and probing a real lane necessarily crosses the
//   network.
// Constraints: credentials via env-ref only (ANTHROPIC_API_KEY); without
//   it, this test reports an explicit skip reason and never silently
//   passes as a real-counterpart proof, and it never runs a paid provider
//   call in a CI job that has no credential (mirroring providers/
//   anthropic/integration_test.go's own liveDriver gate).
// SPORT: internal/fleet.bench (ADD, P1-E12-W3-S25-T5).

package fleet

import (
	"os"
	"testing"
)

// TestProbeRealLane is the acceptance-story integration test: probe a
// real, registered anthropic lane via vault credentials and assert the
// result surfaces in the next fleet.sessions SSE event within 5s.
//
// HONEST GAP (recorded, not papered over): this test currently only
// proves the credential-gated skip contract. Wiring it to a genuine
// pkg/provider.ModelExecutor requires a live daemon composition root
// (Conductor constructed, a real anthropic lane registered in
// internal/providers/registry, and the daemon's fleet.sessions SSE
// surface mounted) that does not exist in this repository yet - the same
// gap TestCallerSitesAreWiredOnceTheirFileExists and this ticket's own
// testonly-allow.json entries for RegisterBenchHandlers/WithBenchClock
// document. Vendoring internal/fleet/testdata/bench_anthropic_probe.json
// from a real response requires that same live path and a funded
// ANTHROPIC_API_KEY, neither available in the environment this ticket was
// built in. Building either without a genuine live round-trip would
// violate Art.2's real-counterpart provenance requirement, so neither is
// fabricated here. journals/BLOCKED-P1-E12-W3-S25-T5.md records this gap
// against the ticket's full acceptance story.
func TestProbeRealLane(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("skip: ANTHROPIC_API_KEY is not set; this lane proves the real-counterpart claim and never substitutes a fake for it")
	}
	t.Skip("skip: the live daemon/Conductor/registry composition root this test needs to reach a real lane " +
		"does not exist in this tree yet (see this file's HONEST GAP note and journals/BLOCKED-P1-E12-W3-S25-T5.md); " +
		"never fabricating a pass here")
}
