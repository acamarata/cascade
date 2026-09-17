package sync

// Purpose (this file): the server-primary merge and the journal it leaves.
// SPORT: internal/sync tests (ADD) — P1-E17-W4-S38-T2.

import (
	"testing"

	"github.com/acamarata/cascade/internal/storage"
)

// configDomain is the registered config class these tests merge.
func configDomain(t *testing.T) DomainClass {
	t.Helper()
	dc, ok := Lookup(storage.DomainConfig, "config")
	if !ok {
		t.Fatal("the config domain is not registered; these tests would prove nothing")
	}
	return dc
}

// rec builds one config record.
func rec(id string, revision, hlc uint64, node, hash string) Record {
	return Record{
		Domain: storage.DomainConfig, Subkind: "config", ID: id, Hash: hash,
		Order: OrderKey{Revision: revision, HLC: hlc, NodeID: node},
	}
}

// TestServerPrimaryKeepsTheServerAndJournalsTheLoss is the strategy's whole
// contract: the server wins and the discarded local write is written down
// with both sides' hashes, so an operator can find what went.
func TestServerPrimaryKeepsTheServerAndJournalsTheLoss(t *testing.T) {
	dc := configDomain(t)
	journal := &ConflictJournal{}

	merged := MergeServerPrimary(journal, dc,
		map[string]Record{"k": rec("k", 9, 1, "server", "sha-server")},
		map[string]Record{"k": rec("k", 8, 99, "laptop", "sha-laptop")},
	)
	if got := merged["k"].Hash; got != "sha-server" {
		t.Fatalf("merged hash = %q, want the server's", got)
	}
	entries := journal.List()
	if len(entries) != 1 {
		t.Fatalf("%d journal entr(y/ies), want 1: a discarded write must be recorded", len(entries))
	}
	e := entries[0]
	if e.Resolution != ResolutionServerWon {
		t.Errorf("resolution = %q, want %q", e.Resolution, ResolutionServerWon)
	}
	if e.Loser.Hash != "sha-laptop" || e.Loser.Revision != 8 {
		t.Errorf("loser = %+v, want the discarded local write, identified by hash", e.Loser)
	}
	if e.Winner.Hash != "sha-server" {
		t.Errorf("winner = %+v, want the server's write", e.Winner)
	}
	if e.Strategy != StrategyServerPrimaryLWW {
		t.Errorf("strategy = %q; a reader cannot tell a deliberate overwrite from a bug without it", e.Strategy)
	}
}

// TestAnUncontestedRecordIsNotAConflict keeps the journal readable. A
// journal full of entries where nothing was lost is one nobody reads.
func TestAnUncontestedRecordIsNotAConflict(t *testing.T) {
	dc := configDomain(t)
	journal := &ConflictJournal{}

	merged := MergeServerPrimary(journal, dc,
		map[string]Record{"a": rec("a", 1, 1, "server", "x")},
		map[string]Record{"b": rec("b", 1, 1, "laptop", "y")},
	)
	if len(merged) != 2 {
		t.Fatalf("%d records merged, want both sides' — no silent loss", len(merged))
	}
	if journal.Len() != 0 {
		t.Errorf("%d conflict(s) journaled for two records that never met", journal.Len())
	}
}

// TestIdenticalRecordsAreNotAConflict is the other quiet case: two sides
// that agree exactly.
func TestIdenticalRecordsAreNotAConflict(t *testing.T) {
	dc := configDomain(t)
	journal := &ConflictJournal{}
	same := rec("k", 4, 4, "server", "same")

	merged := MergeServerPrimary(journal, dc,
		map[string]Record{"k": same}, map[string]Record{"k": same})
	if merged["k"].Hash != "same" {
		t.Error("two identical records did not merge to themselves")
	}
	if journal.Len() != 0 {
		t.Errorf("%d conflict(s) journaled where the sides agreed", journal.Len())
	}
}

// TestThreeWayPartitionConvergence is the property that matters most: three
// peers that each saw a different write must all reach the same state, in
// whatever order they merge.
func TestThreeWayPartitionConvergence(t *testing.T) {
	dc := configDomain(t)
	a := map[string]Record{"k": rec("k", 5, 1, "a", "ha")}
	b := map[string]Record{"k": rec("k", 5, 2, "b", "hb")}
	c := map[string]Record{"k": rec("k", 6, 0, "c", "hc")}

	orders := [][]map[string]Record{
		{a, b, c}, {a, c, b}, {b, a, c}, {b, c, a}, {c, a, b}, {c, b, a},
	}
	var first string
	for i, order := range orders {
		journal := &ConflictJournal{}
		got := MergeServerPrimary(journal, dc, MergeServerPrimary(journal, dc, order[0], order[1]), order[2])
		if i == 0 {
			first = got["k"].Hash
			continue
		}
		if got["k"].Hash != first {
			t.Fatalf("merge order %d converged on %q, the first converged on %q", i, got["k"].Hash, first)
		}
	}
	if first != "hc" {
		t.Errorf("converged on %q, want the highest revision's write (hc)", first)
	}
}

// TestServerPrimaryIsIdempotent states merge(A, A) == A. A merge that was
// not idempotent would drift every time a peer re-synced an unchanged
// state.
func TestServerPrimaryIsIdempotent(t *testing.T) {
	dc := configDomain(t)
	side := map[string]Record{
		"a": rec("a", 1, 1, "n", "ha"),
		"b": rec("b", 2, 2, "m", "hb"),
	}
	journal := &ConflictJournal{}
	got := MergeServerPrimary(journal, dc, side, side)
	if len(got) != len(side) {
		t.Fatalf("merge(A, A) has %d records, A has %d", len(got), len(side))
	}
	for id, want := range side {
		if got[id].Hash != want.Hash {
			t.Errorf("record %s = %q, want %q", id, got[id].Hash, want.Hash)
		}
	}
	if journal.Len() != 0 {
		t.Errorf("merging a side with itself journaled %d conflict(s)", journal.Len())
	}
}

// TestADeleteIsAnOrdinaryValueInConfig pins the difference from the record
// domains: a config key deleted and then re-set at a higher revision should
// be set, not gone.
func TestADeleteIsAnOrdinaryValueInConfig(t *testing.T) {
	dc := configDomain(t)
	deleted := rec("k", 3, 0, "a", "")
	deleted.Tombstone = true
	reset := rec("k", 4, 0, "b", "new")

	journal := &ConflictJournal{}
	merged := MergeServerPrimary(journal, dc,
		map[string]Record{"k": reset}, map[string]Record{"k": deleted})
	if merged["k"].Tombstone {
		t.Fatal("a config delete dominated a later write; config has no tombstone dominance")
	}
}
