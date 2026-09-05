package forget

// Forgetting one record must take exactly that record. These tests build a
// tree with lexical neighbours, a same-named record in another kind, a
// consolidation account for an unrelated group, a staleness queue and a
// candidate ledger entry, then forget one address and prove every other
// file in the tree is byte-identical afterwards.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/memory"
)

// TestForgetLeavesEverySiblingAlone is the blast-radius test.
func TestForgetLeavesEverySiblingAlone(t *testing.T) {
	f := newFixture(t)
	target := f.remember(t, memory.KindProject, "alpha", "the alpha body mentions pelicans\n")
	neighbours := []string{
		f.remember(t, memory.KindProject, "alphabet", "the alphabet body mentions cormorants\n"),
		f.remember(t, memory.KindProject, "alph", "the alph body mentions godwits\n"),
		f.remember(t, memory.KindUser, "alpha", "a user record that shares the name only\n"),
		f.remember(t, memory.KindReference, "zeta", "the zeta body mentions avocets\n"),
	}
	seedUnrelatedArtifacts(t, f)
	before := treeSnapshot(t, f.base)

	f.mustForget(t, target, "asked to")

	assertNeighboursIntact(t, f, neighbours)
	assertOnlyTargetChanged(t, before, treeSnapshot(t, f.base))
}

// assertNeighboursIntact re-reads every neighbour through the store and
// the index, so the check covers both the files and the derived rows.
func assertNeighboursIntact(t *testing.T, f *fixture, ids []string) {
	t.Helper()
	for _, id := range ids {
		kind, name, err := memory.ParseAddress(id)
		if err != nil {
			t.Fatalf("parsing %s: %v", id, err)
		}
		if _, rerr := f.store.Read(context.Background(), kind, name); rerr != nil {
			t.Errorf("neighbour %s became unreadable: %v", id, rerr)
		}
	}
	for _, term := range []string{"cormorants", "godwits", "avocets"} {
		if hits := f.searchHits(t, term); len(hits) != 1 {
			t.Errorf("query %q returned %d hits after forgetting a neighbour, want 1", term, len(hits))
		}
	}
	if hits := f.searchHits(t, "pelicans"); len(hits) != 0 {
		t.Errorf("the forgotten record is still findable: %+v", hits)
	}
}

// assertOnlyTargetChanged proves the file tree moved in exactly the two
// ways a forget is allowed to move it: the target's record file gone, and
// its tombstone plus its account added.
func assertOnlyTargetChanged(t *testing.T, before, after map[string]string) {
	t.Helper()
	added := map[string]bool{
		filepath.Join("project", "alpha.md.tombstone"):             true,
		filepath.Join("forgotten", "project", "alpha.forget.json"): true,
	}
	removed := map[string]bool{filepath.Join("project", "alpha.md"): true}
	for path, body := range before {
		switch {
		case removed[path]:
			if _, still := after[path]; still {
				t.Errorf("%s survived the forget", path)
			}
		case after[path] != body:
			t.Errorf("%s changed, but a forget of another record must not touch it", path)
		}
	}
	for path := range after {
		if _, known := before[path]; !known && !added[path] {
			t.Errorf("%s appeared, and a forget adds only a tombstone and an account", path)
		}
	}
}

// seedUnrelatedArtifacts writes the three things a forget must be able to
// see and leave alone: a consolidation account for a group this record is
// not in, a staleness queue, and a candidate ledger entry.
func seedUnrelatedArtifacts(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	seed := func(rel, body string) {
		t.Helper()
		if err := writeAtomic(filepath.Join(f.base, rel), []byte(body)); err != nil {
			t.Fatalf("seeding %s: %v", rel, err)
		}
	}
	seed(filepath.Join("consolidations", "project", "zeta.consolidation.json"),
		`{"format":1,"survivor":"project/zeta","members":[{"id":"project/omega"}]}`+"\n")
	seed(filepath.Join("staleness", "project.staleness.json"),
		`{"format":1,"kind":"project","ids":["project/alphabet"]}`+"\n")

	ledger := memory.NewFileCandidateLedger(f.base, f.store, f.clock, nil)
	draft := memory.MemoryEntry{
		Name: "gamma", Kind: memory.KindFeedback, Body: "an unrelated candidate draft\n",
		Description: "gamma", ScopeRef: "local", Confidence: 1,
		Provenance: memory.Provenance{Origin: memory.OriginSession, SessionID: "s-9"},
	}
	if _, err := ledger.Observe(ctx, memory.Observation{SessionID: "s-9", Draft: draft}); err != nil {
		t.Fatalf("seeding a candidate: %v", err)
	}
}

// TestForgetReportsTheCandidateLedgerItCannotReach is the honesty test for
// this pipeline's largest gap. A promoted candidate keeps a draft that
// repeats the record's text, the ledger has no delete, and the outcome
// must say so on every call rather than letting the omission pass.
func TestForgetReportsTheCandidateLedgerItCannotReach(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ledger := memory.NewFileCandidateLedger(f.base, f.store, f.clock, nil)
	draft := memory.MemoryEntry{
		Name: "delta", Kind: memory.KindProject, Body: "the delta body mentions puffins\n",
		Description: "delta", ScopeRef: "local", Confidence: 1,
		Provenance: memory.Provenance{Origin: memory.OriginSession, SessionID: "s-1"},
	}
	for _, session := range []string{"s-1", "s-2", "s-3"} {
		if _, err := ledger.Observe(ctx, memory.Observation{SessionID: session, Draft: draft}); err != nil {
			t.Fatalf("observing in %s: %v", session, err)
		}
	}
	if _, _, err := memory.NewPromotionLadder(ledger).Observe(ctx,
		memory.Observation{SessionID: "s-4", Draft: draft}); err != nil {
		t.Fatalf("promoting: %v", err)
	}
	f.project(t)

	out := f.mustForget(t, memory.Address(memory.KindProject, "delta"), "asked to")

	tr := traceFor(t, out, "candidate ledger and review queue")
	if tr.Disposition != memory.ForgetUnreachable {
		t.Fatalf("ledger disposition = %q, want %q", tr.Disposition, memory.ForgetUnreachable)
	}
	// The claim in that trace has to be true, not merely cautious: the
	// draft really is still on disk with the record's text in it.
	found := false
	if err := walk(f.base, func(path string) {
		if strings.Contains(path, "candidates") {
			found = true
		}
	}); err != nil {
		t.Fatalf("walking the tree: %v", err)
	}
	if !found {
		t.Fatal("no candidate record on disk, so the trace overstates what survives")
	}
	if _, err := ledger.Get(ctx, memory.KindProject, "delta"); err != nil {
		t.Fatalf("the candidate the trace says survives is gone: %v", err)
	}
}
