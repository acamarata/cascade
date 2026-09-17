package sync

// Purpose (this file): what the drill leaves behind for a person — the
//   conflict journal, and the elevated resolution that discards the
//   server's copy.
//
// S-38.T3 IS NOT IN THIS TICKET'S DEP CHAIN, and the contract says no CLI
//   verb is required here. It has since landed, so these assertions go
//   through the same `sync.Deps` the CLI and the RPC both use rather than
//   through a second path written for the drill: an acceptance that
//   proved a resolution nobody's surface performs would be proving the
//   wrong thing.
//
// SPORT: sync/acceptance-drill resolve (ADD) — P1-E17-W4-S38-T5.

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// drillDeps is the resolve surface over a peer's own journal.
func drillDeps(peer *acceptancePeer, gate ElevationGate) Deps {
	return Deps{Engine: peer.engine, PeerTier: nodes.TierController, Gate: gate}
}

// TestEveryDrillConflictIsJournaledWithBothSides is the evidence an
// operator reads after the drill: what collided, which merge decided, what
// won and what went.
func TestEveryDrillConflictIsJournaledWithBothSides(t *testing.T) {
	a, b := newAcceptancePeer(t, "laptop"), newAcceptancePeer(t, "server")
	runDrillScript(t, localCarrier, a, b, nodes.TierController)

	entries := append(a.engine.Conflicts().List(), b.engine.Conflicts().List()...)
	if len(entries) == 0 {
		t.Fatal("the drill journaled nothing at all")
	}
	for _, c := range entries {
		if c.RecordID == "" {
			t.Error("a journal entry names no record")
		}
		if c.Domain == "" || c.Subkind == "" {
			t.Errorf("conflict %q names no domain", c.RecordID)
		}
		if c.Strategy == "" {
			t.Errorf("conflict %q does not say which merge decided it", c.RecordID)
		}
		if c.Resolution == "" {
			t.Errorf("conflict %q records no resolution", c.RecordID)
		}
		// A refusal has no winner by design; anything else must name one.
		if c.Resolution != ResolutionRefused && c.Winner.NodeID == "" && c.Winner.Ref == "" {
			t.Errorf("conflict %q was resolved but names no winner", c.RecordID)
		}
	}
}

// TestKeepingTheServerSideIsNotElevated: re-affirming what the merge
// already decided changes nothing and asks nobody for anything.
func TestKeepingTheServerSideIsNotElevated(t *testing.T) {
	peer := newAcceptancePeer(t, "server")
	other := newAcceptancePeer(t, "laptop")
	runDrillScript(t, localCarrier, other, peer, nodes.TierController)
	conflicts := peer.engine.Conflicts().List()
	if len(conflicts) == 0 {
		t.Skip("this run journaled no conflict on the server side")
	}

	gate := &drillGate{}
	res, err := drillDeps(peer, gate).Resolve(context.Background(), conflicts[0].RecordID, KeepServer)
	if err != nil {
		t.Fatalf("accepting the merge's own decision was refused: %v", err)
	}
	if res.Elevated {
		t.Error("keeping the server's side reported itself as elevated")
	}
	if len(gate.verbs) != 0 {
		t.Errorf("the gate was consulted for a no-op: %v", gate.verbs)
	}
}

// TestDiscardingTheServerCopyRoutesTheElevatedFlow is the 06 §5.14
// assertion, in both directions: refused without authorization, honored
// with it.
func TestDiscardingTheServerCopyRoutesTheElevatedFlow(t *testing.T) {
	peer := newAcceptancePeer(t, "server")
	other := newAcceptancePeer(t, "laptop")
	runDrillScript(t, localCarrier, other, peer, nodes.TierController)
	conflicts := peer.engine.Conflicts().List()
	if len(conflicts) == 0 {
		t.Skip("this run journaled no conflict on the server side")
	}
	recordID := conflicts[0].RecordID

	// Refused when the gate says no.
	denied := errors.New("no attestation")
	_, err := drillDeps(peer, &drillGate{err: denied}).Resolve(context.Background(), recordID, KeepLocal)
	if err == nil {
		t.Fatal("the server's copy was discarded without authorization")
	}

	// Refused when there is no gate at all: a machine that cannot check
	// must not be the one that allows it.
	if _, err := drillDeps(peer, nil).Resolve(context.Background(), recordID, KeepLocal); err == nil {
		t.Fatal("a peer with no elevation gate discarded the server's copy")
	} else if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindElevationRequired {
		t.Errorf("kind = %v (ok=%v), want KindElevationRequired", kind, ok)
	}

	// Honored when it says yes, and the result RECORDS that the gate ran.
	gate := &drillGate{}
	res, err := drillDeps(peer, gate).Resolve(context.Background(), recordID, KeepLocal)
	if err != nil {
		t.Fatalf("an authorized resolution was refused: %v", err)
	}
	if !res.Elevated {
		t.Error("the result does not record that the gate ran")
	}
	if len(gate.verbs) != 1 || gate.verbs[0] != ElevatedVerbResolve {
		t.Errorf("the gate saw %v, want exactly [%s]", gate.verbs, ElevatedVerbResolve)
	}
}

// TestResolvingSomethingTheDrillNeverJournaledIsRefused keeps the resolve
// surface from reporting that it settled a conflict nobody had.
func TestResolvingSomethingTheDrillNeverJournaledIsRefused(t *testing.T) {
	peer := newAcceptancePeer(t, "server")
	_, err := drillDeps(peer, &drillGate{}).Resolve(context.Background(), "never-collided", KeepServer)
	if err == nil {
		t.Fatal("a conflict that never happened was resolved")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Errorf("kind = %v (ok=%v), want KindNotFound", kind, ok)
	}
}

// TestACursorNeverRegressesAcrossTheDrill is the S-38.T1 contract the
// round trip depends on: a cursor that went backwards would re-send
// records the other side had already applied, and an append domain would
// journal every one of them as a fresh conflict.
func TestACursorNeverRegressesAcrossTheDrill(t *testing.T) {
	peer := newAcceptancePeer(t, "laptop")
	ctx := context.Background()
	const domain, subkind = storage.DomainMemory, "memory"

	var last uint64
	for i := range 3 {
		recs := []Record{{
			Domain: domain, Subkind: subkind, ID: "rec-" + string(rune('a'+i)),
			Tier: "internal", Payload: []byte("p"),
		}}
		cur, err := peer.engine.SendBatch(ctx, discardWriter{}, domain, subkind, recs, uint64(i+1), 0)
		if err != nil {
			t.Fatalf("SendBatch %d: %v", i, err)
		}
		if cur.Position <= last && i > 0 {
			t.Fatalf("cursor went from %d to %d; a regress re-sends applied records", last, cur.Position)
		}
		last = cur.Position
	}

	// And an explicit regress is refused rather than accepted as a
	// resync-from-scratch signal.
	if err := peer.engine.Cursors().Advance(ctx, domain, subkind, 1); err == nil {
		t.Fatal("a cursor regress was accepted")
	}
}

// discardWriter is the wire when the drill only cares about the cursor.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// drillGate records what it was asked to authorize.
type drillGate struct {
	verbs []string
	err   error
}

func (g *drillGate) Authorize(_ context.Context, verb string) error {
	g.verbs = append(g.verbs, verb)
	return g.err
}
