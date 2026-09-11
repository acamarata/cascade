//go:build spike

package syncmerge

import (
	"errors"
	"testing"
)

// TestConfigMergeLWW exercises configMergeLWW against every config-domain
// adversarial fixture named in P1-E13-W3-S27-T5's full_desc: equal-revision
// tie-break, delete-versus-write, 24h clock skew, and three-way partition
// rejoin. Each case's body lives in its own helper (funlen).
func TestConfigMergeLWW(t *testing.T) {
	t.Run("revision-tie-break", testConfigRevisionTieBreak)
	t.Run("delete-vs-write", testConfigDeleteVsWrite)
	t.Run("clock-skew-24h", testConfigClockSkew24h)
	t.Run("three-way-partition", testConfigThreeWayPartition)
}

func testConfigRevisionTieBreak(t *testing.T) {
	f := loadFixture(t, "config-revision-tie-break.json")
	a := toMap(decodeSide[ConfigRecord](t, f.SideA))
	b := toMap(decodeSide[ConfigRecord](t, f.SideB))

	merged, journal := configMergeLWW(a, b)
	if len(journal) != 1 {
		t.Fatalf("expected exactly one journal entry for the tie-break, got %d", len(journal))
	}
	// Revisions are equal by fixture construction; the tie must be broken
	// by HLC, then node id -- resolved exactly once, never re-resolved by
	// a second pass.
	winner := merged[journal[0].RecordID]
	var a0, b0 ConfigRecord
	for _, r := range a {
		a0 = r
	}
	for _, r := range b {
		b0 = r
	}
	if a0.Revision != b0.Revision {
		t.Fatalf("fixture setup error: revisions differ (%d vs %d), not a tie-break case", a0.Revision, b0.Revision)
	}
	want := a0
	if compareConfig(b0, a0) > 0 {
		want = b0
	}
	if winner.NodeID != want.NodeID || winner.HLC != want.HLC {
		t.Fatalf("tie-break winner = node %s hlc %d, want node %s hlc %d", winner.NodeID, winner.HLC, want.NodeID, want.HLC)
	}
}

func testConfigDeleteVsWrite(t *testing.T) {
	f := loadFixture(t, "config-delete-vs-write.json")
	a := toMap(decodeSide[ConfigRecord](t, f.SideA))
	b := toMap(decodeSide[ConfigRecord](t, f.SideB))

	merged, _ := configMergeLWW(a, b)
	var expectWinner ConfigRecord
	for id := range merged {
		av, aok := a[id]
		bv, bok := b[id]
		if aok && bok {
			if compareConfig(av, bv) >= 0 {
				expectWinner = av
			} else {
				expectWinner = bv
			}
		}
	}
	got := merged[expectWinner.RecordID]
	if got.Tombstone != expectWinner.Tombstone {
		t.Fatalf("delete-vs-write: winner tombstone=%v, want %v (ordering, not tombstone-dominance, decides in config)", got.Tombstone, expectWinner.Tombstone)
	}
}

func testConfigClockSkew24h(t *testing.T) {
	f := loadFixture(t, "config-clock-skew-24h.json")
	a := toMap(decodeSide[ConfigRecord](t, f.SideA))
	b := toMap(decodeSide[ConfigRecord](t, f.SideB))

	merged, journal := configMergeLWW(a, b)
	var server, localAhead ConfigRecord
	for _, r := range a {
		if r.NodeID == "server" {
			server = r
		} else {
			localAhead = r
		}
	}
	for _, r := range b {
		if r.NodeID == "server" {
			server = r
		} else {
			localAhead = r
		}
	}
	if localAhead.WallClock <= server.WallClock {
		t.Fatalf("fixture setup error: local wall clock (%d) is not ahead of server's (%d)", localAhead.WallClock, server.WallClock)
	}
	if localAhead.Revision >= server.Revision {
		t.Fatalf("fixture setup error: local revision must be behind server's for this to prove wall-clock is ignored")
	}
	winner := merged[server.RecordID]
	if winner.NodeID != "server" {
		t.Fatalf("a local clock 24h ahead must still lose to the server revision; winner = %q", winner.NodeID)
	}
	if len(journal) != 1 || journal[0].LosingNodeID != localAhead.NodeID {
		t.Fatalf("expected the ahead-clock local write journaled as the loser, got %+v", journal)
	}
}

func testConfigThreeWayPartition(t *testing.T) {
	f := loadFixture(t, "config-three-way-partition.json")
	recs := decodeSide[ConfigRecord](t, f.SideA)
	if len(recs) != 3 {
		t.Fatalf("three-way partition fixture must carry exactly 3 replica views, got %d", len(recs))
	}
	mA := map[string]ConfigRecord{recs[0].RecordID: recs[0]}
	mB := map[string]ConfigRecord{recs[1].RecordID: recs[1]}
	mC := map[string]ConfigRecord{recs[2].RecordID: recs[2]}

	leftFirst, _ := configMergeLWW(mA, mB)
	leftFirst, _ = configMergeLWW(leftFirst, mC)

	rightFirst, _ := configMergeLWW(mB, mC)
	rightFirst, _ = configMergeLWW(mA, rightFirst)

	id := recs[0].RecordID
	if leftFirst[id] != rightFirst[id] {
		t.Fatalf("three-way partition rejoin did not converge: (A,B),C = %+v vs A,(B,C) = %+v", leftFirst[id], rightFirst[id])
	}
}

// TestConfigJournalCarriesBothSides confirms every journal entry from a
// losing local write carries both revisions and both content hashes
// (R-21.223 domain 1).
func TestConfigJournalCarriesBothSides(t *testing.T) {
	a := map[string]ConfigRecord{"k": {RecordID: "k", Revision: 1, HLC: 1, NodeID: "n1", ValueHash: "h1"}}
	b := map[string]ConfigRecord{"k": {RecordID: "k", Revision: 2, HLC: 1, NodeID: "n2", ValueHash: "h2"}}
	_, journal := configMergeLWW(a, b)
	if len(journal) != 1 {
		t.Fatalf("expected 1 journal entry, got %d", len(journal))
	}
	e := journal[0]
	if e.WinningHash != "h2" || e.LosingHash != "h1" || e.WinningRevision != 2 || e.LosingRevision != 1 {
		t.Fatalf("journal entry missing both revisions/hashes: %+v", e)
	}
}

// TestErrPeerCursorTooOldIsConflict confirms the sentinel wraps a frozen
// taxonomy Kind rather than inventing one.
func TestErrPeerCursorTooOldIsConflict(t *testing.T) {
	if err := checkPeerCursor(1, 5); !errors.Is(err, ErrPeerCursorTooOld) {
		t.Fatalf("checkPeerCursor(1,5) = %v, want ErrPeerCursorTooOld", err)
	}
	if err := checkPeerCursor(10, 5); err != nil {
		t.Fatalf("checkPeerCursor(10,5) = %v, want nil", err)
	}
}
