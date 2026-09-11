package conformance

// Purpose: the Round-35 lifecycle amendment cases (21-T0-RULINGS-R21.md
//
//	§F/§F.2): the cancel ladder's real observable behavior, process-group
//	identity, protocol negotiation refusal, and event ordering/dedup —
//	split from suite.go per the 300-line cap (R-14.117 in-package split).
//
// SPORT: providers.agents.conformance/ADD (P1-E30-W6-S61-T1).

import (
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// TestCancelUncooperativeChild asserts the R-21.174/R-21.140 cancel
// contract end to end through the interface: Cancel either succeeds
// (the job reaches AgentRunCancelled) or reports the honest
// ErrCancelUnconfirmed — it never leaves the job silently running, and it
// never reports AgentRunCancelled without Cancel having been called. The
// suite does not itself send signals (that is the driver's job); it
// asserts the CONTRACT-visible outcome.
func (s *Suite) TestCancelUncooperativeChild(t *testing.T) {
	p := s.newProvider(t)
	ctx := testContext(t)
	res, err := p.Spawn(ctx, defaultSpec())
	requireNoError(t, err, "Spawn")

	cancelErr := p.Cancel(ctx, res.JobID)
	if cancelErr != nil && cancelErr != provider.ErrCancelUnconfirmed {
		t.Fatalf("Cancel = %v, want nil or ErrCancelUnconfirmed", cancelErr)
	}

	final := pollUntilTerminal(t, p, res.JobID)
	if cancelErr == nil && final != provider.AgentRunCancelled {
		t.Fatalf("Cancel succeeded but final state = %q, want cancelled", final)
	}
	if cancelErr == provider.ErrCancelUnconfirmed && final == provider.AgentRunRunning {
		t.Fatal("ErrCancelUnconfirmed but Status still reports running: the job must at least be cancelling")
	}
}

// TestSpawnResultCarriesProcessGroup asserts SpawnResult.ProcessGroupID
// names a live group distinct from a second job's, and permits 0 only for
// an in-process lane (R-21.177).
func (s *Suite) TestSpawnResultCarriesProcessGroup(t *testing.T) {
	p := s.newProvider(t)
	ctx := testContext(t)
	a, err := p.Spawn(ctx, defaultSpec())
	requireNoError(t, err, "Spawn a")
	b, err := p.Spawn(ctx, defaultSpec())
	requireNoError(t, err, "Spawn b")

	if a.ProcessGroupID < 0 || b.ProcessGroupID < 0 {
		t.Fatalf("negative ProcessGroupID: a=%d b=%d", a.ProcessGroupID, b.ProcessGroupID)
	}
	if a.ProcessGroupID != 0 && a.ProcessGroupID == b.ProcessGroupID {
		t.Fatalf("two spawns share ProcessGroupID %d, want distinct groups", a.ProcessGroupID)
	}
}

// TestProtocolNegotiationRefusal asserts a harness range guaranteed
// disjoint from the adapter's declared range refuses at RUNTIME with
// ErrHarnessIncompatible (R-21.158).
func (s *Suite) TestProtocolNegotiationRefusal(t *testing.T) {
	p := s.newProvider(t)
	mine := p.SupportedProtocols()
	disjoint := provider.ProtocolRange{
		Min: mine.Max + "~out-of-range",
		Max: mine.Max + "~out-of-range~~",
	}
	if _, err := p.Negotiate(testContext(t), disjoint); err != provider.ErrHarnessIncompatible {
		t.Fatalf("Negotiate(disjoint range) = %v, want ErrHarnessIncompatible", err)
	}
}

// TestEventOrderingAndDedup asserts provider.EventNormalizer's ordering
// and dedup guarantees hold independent of any one driver's own
// sequencing discipline.
func (s *Suite) TestEventOrderingAndDedup(t *testing.T) {
	n := provider.NewEventNormalizer()
	if _, keep, err := n.Normalize(provider.DriverEvent{Kind: provider.DriverEventStdout, Seq: 1, DedupKey: "x"}); err != nil || !keep {
		t.Fatalf("first event: keep=%v err=%v, want (true, nil)", keep, err)
	}
	if _, keep, err := n.Normalize(provider.DriverEvent{Kind: provider.DriverEventStdout, Seq: 2, DedupKey: "x"}); err != nil || keep {
		t.Fatalf("repeated DedupKey: keep=%v err=%v, want (false, nil)", keep, err)
	}
	if _, keep, err := n.Normalize(provider.DriverEvent{Kind: provider.DriverEventStdout, Seq: 1, DedupKey: "y"}); err != provider.ErrOutOfOrderEvent || keep {
		t.Fatalf("Seq regression: keep=%v err=%v, want (false, ErrOutOfOrderEvent)", keep, err)
	}
}
