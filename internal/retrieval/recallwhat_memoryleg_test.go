package retrieval

// Purpose: TestMemoryOutcome_ExpiredEntryDemoted and
// TestMemoryOutcome_InScopeRowPastOldKReturned (P1-E07-W5-S92-T1), split
// out of recallwhat_filter_test.go purely for the 300-line cap -- both are
// tests in this package that need a real *memory.ProjectionJob over a real
// SQLite-backed store, rather than fakeMemoryLeg, to prove behaviour that
// only exists inside the real projection's own search.
//
// SPORT: internal.retrieval.RecallWhatService/ADDED (P1-E22-W5-S47-T1);
// CHANGED (P1-E07-W5-S92-T1).

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/providers/sqlite"
)

// expiredMemoryFixture writes two real records ("old" past its TTL by
// query time, "new" with none) to a real *memory.FileStore, projects them
// into a real SQLite-backed *memory.ProjectionJob, and advances clock past
// "old"'s TTL. Split out of the test purely for the 50-line function cap.
func expiredMemoryFixture(t *testing.T) (*memory.ProjectionJob, *testkit.FrozenClock) {
	t.Helper()
	ctx := context.Background()
	clock := testClock()
	dir := t.TempDir()
	files := memory.NewFileStore(filepath.Join(dir, "memory"), clock)
	driver, err := sqlite.Open(ctx, filepath.Join(dir, "cascade.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	job := memory.NewProjectionJob(files, driver, nil, nil, clock)

	expiry := clock.Now().Add(time.Hour) // future at write time -- Validate refuses an already-past TTL
	if err := files.Write(ctx, memory.MemoryEntry{
		Name: "old", Kind: memory.KindProject, Body: "note pelicans", ScopeRef: "proj1",
		Provenance: memory.Provenance{Origin: memory.OriginSession}, ExpiresAt: &expiry,
	}); err != nil {
		t.Fatalf("Write(old): %v", err)
	}
	if err := files.Write(ctx, memory.MemoryEntry{
		Name: "new", Kind: memory.KindProject, Body: "note pelicans", ScopeRef: "proj1",
		Provenance: memory.Provenance{Origin: memory.OriginSession},
	}); err != nil {
		t.Fatalf("Write(new): %v", err)
	}
	if _, err := job.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	clock.Advance(2 * time.Hour) // "old" is now past its TTL
	return job, clock
}

// TestMemoryOutcome_ExpiredEntryDemoted is D6/Q6's rewrite against the
// REAL *memory.ProjectionJob (fakeMemoryLeg's literal ExpiresAtUnixNano
// bools proved nothing about the real leg, which the confirming review
// found never actually returned an expired row -- searchIndex excluded it
// unconditionally, so the real production path never hit this code at
// all). The past-ExpiresAt row must (1) still be RETURNED by the leg
// (SearchIncludingExpired, not excluded), (2) be marked Expired, and (3)
// never rank above the live row once demoteSupersededAndExpired runs,
// even when its raw RRF score is higher.
func TestMemoryOutcome_ExpiredEntryDemoted(t *testing.T) {
	job, clock := expiredMemoryFixture(t)
	svc, err := NewRecallWhatService(nil, nil, job, rrf.Params{}, clock, resolverFor("proj1"), testEgress(t))
	if err != nil {
		t.Fatalf("NewRecallWhatService: %v", err)
	}
	out := svc.memoryOutcome(context.Background(), RecallWhatRequest{Query: "pelicans", Scope: "proj1"}, 10)
	if out.err != nil {
		t.Fatalf("memoryOutcome: %v", out.err)
	}
	// R-16.7 demotes an expired row below its peers; it does not exclude
	// it outright, so BOTH rows must still be candidates here.
	oldID, newID := DomainMemory+":project/old", DomainMemory+":project/new"
	if len(out.list.Hits) != 2 {
		t.Fatalf("want both rows as candidates from the real leg (expiry demotes, does not exclude): hits=%+v", out.list.Hits)
	}
	if !out.meta[oldID].Expired {
		t.Errorf("the past-ExpiresAt row (clock=%v) was not marked Expired by the real leg", clock.Now())
	}
	if out.meta[newID].Expired {
		t.Errorf("the no-TTL row was wrongly marked Expired")
	}

	// A higher raw score must not rescue the expired row: R-16.7 says it
	// never ranks above a live peer, full stop.
	fused := []rrf.FusedResult{{ChunkID: oldID, Score: 0.99}, {ChunkID: newID, Score: 0.10}}
	got := demoteSupersededAndExpired(fused, out.meta)
	if len(got) != 2 || got[0].ChunkID != newID || got[1].ChunkID != oldID {
		t.Fatalf("demoteSupersededAndExpired = %v, want the live row %q ranked above the expired row %q despite its lower score",
			chunkIDs(got), newID, oldID)
	}
}

// TestMemoryOutcome_InScopeRowPastOldKReturned is P1-E07-W5-S92-T1's test
// for the scope-before-k fix (PCI s47t1-memory-scope-after-k): four
// out-of-scope records are written with names that sort AHEAD of the one
// in-scope record in raw id order ("project/a1".."project/a4" before
// "project/z-target"), and k is set below that out-of-scope count. The OLD
// order -- fetch k rows, then drop out-of-scope ones -- would fill the
// whole k=3 window with a1/a2/a3 and never reach z-target at all. The
// fixed leg narrows the real projection's own search to the resolved
// scope BEFORE its k cap applies, so the in-scope row is returned despite
// ranking behind more than k out-of-scope rows, and no out-of-scope row is
// ever returned.
func TestMemoryOutcome_InScopeRowPastOldKReturned(t *testing.T) {
	ctx := context.Background()
	clock := testClock()
	dir := t.TempDir()
	files := memory.NewFileStore(filepath.Join(dir, "memory"), clock)
	driver, err := sqlite.Open(ctx, filepath.Join(dir, "cascade.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	job := memory.NewProjectionJob(files, driver, nil, nil, clock)

	for _, name := range []string{"a1", "a2", "a3", "a4"} {
		if err := files.Write(ctx, memory.MemoryEntry{
			Name: name, Kind: memory.KindProject, Body: "shared term", ScopeRef: "other-proj",
			Provenance: memory.Provenance{Origin: memory.OriginSession},
		}); err != nil {
			t.Fatalf("Write(%s): %v", name, err)
		}
	}
	if err := files.Write(ctx, memory.MemoryEntry{
		Name: "z-target", Kind: memory.KindProject, Body: "shared term", ScopeRef: "proj1",
		Provenance: memory.Provenance{Origin: memory.OriginSession},
	}); err != nil {
		t.Fatalf("Write(z-target): %v", err)
	}
	if _, err := job.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	svc, err := NewRecallWhatService(nil, nil, job, rrf.Params{}, clock, resolverFor("proj1"), testEgress(t))
	if err != nil {
		t.Fatalf("NewRecallWhatService: %v", err)
	}
	const k = 3 // fewer than the 4 out-of-scope rows ranked ahead of z-target
	out := svc.memoryOutcome(ctx, RecallWhatRequest{Query: "shared", Scope: "proj1"}, k)
	if out.err != nil {
		t.Fatalf("memoryOutcome: %v", out.err)
	}
	wantID := DomainMemory + ":project/z-target"
	if len(out.list.Hits) != 1 || out.list.Hits[0].ChunkID != wantID {
		t.Fatalf("memoryOutcome(k=%d) hits = %+v, want exactly %q despite 4 out-of-scope rows ranked ahead of it",
			k, out.list.Hits, wantID)
	}
}
