package conversation

// Purpose: TestAcceptancePaginationRetention -- S-44.T5 acceptance
//   criterion 4. Seeds one real store (real modernc-sqlite file,
//   t.TempDir()) with a thread of turns whose content marks a subset with
//   a sentinel token, then drives the REAL Adapter surface search.go/
//   pagination.go/retention.go ship (SearchTurns, ListTurnsPage,
//   PruneTurns -- never a re-derivation of their SQL): the FTS5 search
//   for the sentinel returns exactly the seeded matching turns, no more
//   and no fewer; pagination walks the full seeded thread across page
//   boundaries with zero loss and zero duplication; retention prune
//   removes exactly the turns older than the configured window and
//   leaves the rest untouched.
// Named failing inputs: a search that returns 0 or N+1 hits for a
//   sentinel seeded into exactly N turns; a pagination walk whose
//   collected turn-ID set differs in size or membership from the seeded
//   set; a retention prune whose tombstone count is not exactly the
//   count of turns older than the cutoff.
// SPORT: internal.conversation/acceptance (ADDED, tests-only) (P1-E20-W5-S44-T5).

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"
)

// acceptancePaginationThread is the thread every subtest below seeds
// directly into the store (bypassing chat.append_turn's RPC path, whose
// clock is frozen -- these subtests need explicit, distinct CreatedAt
// values per turn, matching retention_test.go's own seedTurn precedent
// for the identical reason).
const acceptancePaginationThread = "th-accept-pagination"

// acceptanceSentinelQuery/acceptanceTotalTurns/acceptanceMatchingSeqs:
// package-level (not TestAcceptancePaginationRetention-local) because the
// search/pagination/retention assertions below are split into their own
// Art.10.3-sized helper functions and all need these three values.
const (
	acceptanceSentinelQuery = "acceptsentinelquery44t5"
	acceptanceTotalTurns    = 25
	acceptanceMatchingSeqs  = 4 // turns whose content carries the sentinel token.
)

// seedAcceptanceTurn appends one turn with a single text segment directly
// through the real Store (never the RPC path) so its CreatedAt is
// caller-controlled -- required for the retention subtest's age cutoff.
func seedAcceptanceTurn(t *testing.T, store Store, threadID string, seq int64, content string, createdAt int64) Turn {
	t.Helper()
	turn := Turn{ID: NewTurnID(threadID, seq, RoleUser), ThreadID: threadID, Seq: seq, Role: RoleUser, CreatedAt: createdAt}
	if err := store.AppendTurn(context.Background(), turn); err != nil {
		t.Fatalf("seed AppendTurn(seq=%d): %v", seq, err)
	}
	seg := Segment{ID: NewSegmentID(turn.ID, 0, SegmentText), TurnID: turn.ID, Seq: 0, Kind: SegmentText, Content: content, CreatedAt: createdAt}
	if err := store.AppendSegment(context.Background(), seg); err != nil {
		t.Fatalf("seed AppendSegment(seq=%d): %v", seq, err)
	}
	return turn
}

// seedAcceptancePaginationCorpus seeds acceptanceTotalTurns turns into
// store, acceptanceMatchingSeqs of which carry acceptanceSentinelQuery, and
// returns the seeded turns, the matching-turn-id set, and the base
// CreatedAt epoch the retention subtest anchors "now" to. Split out of
// TestAcceptancePaginationRetention (Art.10.3: functions <=50 lines).
func seedAcceptancePaginationCorpus(t *testing.T, store Store) (seeded []Turn, matching map[string]bool, baseCreatedAt int64) {
	t.Helper()
	seeded = make([]Turn, 0, acceptanceTotalTurns)
	matching = make(map[string]bool, acceptanceMatchingSeqs)
	baseCreatedAt = 1_700_000_000 // fixed epoch base; each turn one day apart.
	for i := int64(0); i < acceptanceTotalTurns; i++ {
		content := fmt.Sprintf("plain content for turn %d", i)
		if i < acceptanceMatchingSeqs {
			content = fmt.Sprintf("turn %d carries the %s token", i, acceptanceSentinelQuery)
		}
		turn := seedAcceptanceTurn(t, store, acceptancePaginationThread, i, content, baseCreatedAt+i*86400)
		seeded = append(seeded, turn)
		if i < acceptanceMatchingSeqs {
			matching[turn.ID] = true
		}
	}
	return seeded, matching, baseCreatedAt
}

// acceptancePaginationAssertSearchHits: the FTS5 search for the sentinel
// returns exactly the seeded matching turns, no more and no fewer.
func acceptancePaginationAssertSearchHits(ctx context.Context, t *testing.T, adapter *Adapter, matching map[string]bool) {
	t.Helper()
	hits, err := adapter.SearchTurns(ctx, acceptanceSentinelQuery, SearchFilter{ThreadID: acceptancePaginationThread, Limit: 100})
	if err != nil {
		t.Fatalf("SearchTurns(%q): %v", acceptanceSentinelQuery, err)
	}
	if len(hits) != acceptanceMatchingSeqs {
		t.Fatalf("SearchTurns(%q) returned %d hits, want exactly %d (the seeded matching turns)", acceptanceSentinelQuery, len(hits), acceptanceMatchingSeqs)
	}
	for _, h := range hits {
		if !matching[h.Turn.ID] {
			t.Fatalf("SearchTurns(%q) returned turn %s, which was not seeded with the sentinel -- a false-positive match", acceptanceSentinelQuery, h.Turn.ID)
		}
	}
}

// acceptancePaginationAssertSearchEmpty: named failing input for a search
// that matches everything -- an unseeded token must return zero hits.
func acceptancePaginationAssertSearchEmpty(ctx context.Context, t *testing.T, adapter *Adapter) {
	t.Helper()
	hits, err := adapter.SearchTurns(ctx, "nothingintheseedcorpusshouldmatchthis", SearchFilter{ThreadID: acceptancePaginationThread, Limit: 100})
	if err != nil {
		t.Fatalf("SearchTurns(unseeded token): %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("SearchTurns(unseeded token) returned %d hits, want 0 -- named failing input for a search that matches everything", len(hits))
	}
}

// acceptancePaginationAssertWalk walks the full seeded thread across page
// boundaries and asserts zero loss and zero duplication.
func acceptancePaginationAssertWalk(ctx context.Context, t *testing.T, adapter *Adapter, seeded []Turn) {
	t.Helper()
	seen := make(map[string]int, acceptanceTotalTurns)
	var cursor Cursor
	pages := 0
	for {
		page, err := adapter.ListTurnsPage(ctx, acceptancePaginationThread, PaginationFilter{Cursor: cursor, Limit: 7})
		if err != nil {
			t.Fatalf("ListTurnsPage(cursor=%q): %v", cursor, err)
		}
		if len(page.Turns) == 0 && page.NextCursor == "" && pages == 0 {
			t.Fatal("ListTurnsPage returned an empty first page for a thread with seeded turns")
		}
		for _, turn := range page.Turns {
			seen[turn.ID]++
		}
		pages++
		if pages > acceptanceTotalTurns { // a cursor cycle would otherwise hang the test forever.
			t.Fatalf("ListTurnsPage did not terminate after %d pages walking %d turns -- NextCursor never went empty", pages, acceptanceTotalTurns)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != acceptanceTotalTurns {
		t.Fatalf("pagination walk collected %d distinct turn ids, want %d (loss)", len(seen), acceptanceTotalTurns)
	}
	for _, turn := range seeded {
		switch n := seen[turn.ID]; {
		case n == 0:
			t.Fatalf("pagination walk never returned seeded turn %s (loss)", turn.ID)
		case n > 1:
			t.Fatalf("pagination walk returned seeded turn %s %d times, want exactly 1 (duplication)", turn.ID, n)
		}
	}
}

// acceptancePaginationPrune runs PruneTurns once and asserts both its own
// reported count and the real conversation_turn_tombstone row count match
// wantTombstoned exactly.
func acceptancePaginationPrune(ctx context.Context, t *testing.T, adapter *Adapter, db *sql.DB, wantTombstoned int) {
	t.Helper()
	res, err := adapter.PruneTurns(ctx, RetentionPolicy{AgeMaxDays: 10, TurnsMaxPerThread: 1})
	if err != nil {
		t.Fatalf("PruneTurns: %v", err)
	}
	if res.Tombstoned != wantTombstoned {
		t.Fatalf("PruneTurns tombstoned %d rows, want exactly %d (turns older than the 10-day cutoff)", res.Tombstoned, wantTombstoned)
	}
	if n := countTombstones(t, db); n != wantTombstoned {
		t.Fatalf("conversation_turn_tombstone has %d rows (real table, not PruneResult's own count), want %d", n, wantTombstoned)
	}
}

// acceptancePaginationAssertUnmodified confirms PruneTurns tombstones
// logically -- it must never delete the row (retention.go's own
// documented contract) -- and every in-window turn keeps its original,
// unmodified content.
func acceptancePaginationAssertUnmodified(ctx context.Context, t *testing.T, store Store, seeded []Turn, cutoff int64) {
	t.Helper()
	remaining, err := store.ListTurns(ctx, acceptancePaginationThread)
	if err != nil {
		t.Fatalf("ListTurns after prune: %v", err)
	}
	if len(remaining) != len(seeded) {
		t.Fatalf("ListTurns after prune returned %d turns, want %d -- PruneTurns tombstones logically, it must never delete the row (retention.go's own documented contract)", len(remaining), len(seeded))
	}
	for _, turn := range seeded {
		if turn.CreatedAt >= cutoff {
			segs, err := store.ListSegments(ctx, turn.ID)
			if err != nil || len(segs) != 1 {
				t.Fatalf("in-window turn %s: ListSegments = %v, %v, want exactly 1 unmodified segment", turn.ID, segs, err)
			}
		}
	}
}

// acceptancePaginationAssertRetention: retention prune removes exactly the
// turns older than the configured window and leaves the rest untouched,
// and a second prune against unchanged data is idempotent (tombstones 0
// more rows).
func acceptancePaginationAssertRetention(ctx context.Context, t *testing.T, store Store, db *sql.DB, seeded []Turn, baseCreatedAt int64) {
	t.Helper()
	// "now" = the last seeded turn's instant. AgeMaxDays=10 means any
	// turn more than 10 days older than that qualifies on the age
	// dimension. retention.go's own findPruneCandidates ANDs age with
	// "at least TurnsMaxPerThread newer turns exist in this thread" --
	// TurnsMaxPerThread=1 (not acceptanceTotalTurns: a turn ranking beyond
	// the PER-THREAD-MOST-RECENT count means "has >= N newer turns", so a
	// SMALL N is the lenient setting) means every turn except the single
	// most-recent one qualifies on that dimension, leaving the age cutoff
	// as the only boundary this subtest actually exercises.
	now := baseCreatedAt + (acceptanceTotalTurns-1)*86400
	clock := fixedClockAt(now)
	retentionAdapter := NewAdapter(store, nil, passthroughSubst{}, clock, "")

	cutoff := now - 10*86400
	wantTombstoned := 0
	for _, turn := range seeded {
		if turn.CreatedAt < cutoff {
			wantTombstoned++
		}
	}
	if wantTombstoned == 0 || wantTombstoned == len(seeded) {
		t.Fatalf("test setup: wantTombstoned = %d out of %d seeded turns, want a mix (neither 0 nor all) so this assertion is not vacuous", wantTombstoned, len(seeded))
	}

	acceptancePaginationPrune(ctx, t, retentionAdapter, db, wantTombstoned)
	acceptancePaginationAssertUnmodified(ctx, t, store, seeded, cutoff)

	// Idempotent: pruning again against unchanged data tombstones 0 more
	// rows -- the exact-count AC's second half.
	res2, err := retentionAdapter.PruneTurns(ctx, RetentionPolicy{AgeMaxDays: 10, TurnsMaxPerThread: 1})
	if err != nil {
		t.Fatalf("second PruneTurns: %v", err)
	}
	if res2.Tombstoned != 0 {
		t.Fatalf("second PruneTurns tombstoned %d rows, want 0 (idempotency)", res2.Tombstoned)
	}
}

func TestAcceptancePaginationRetention(t *testing.T) {
	store, db := newTestStoreWithDB(t)
	adapter := NewAdapter(store, nil, passthroughSubst{}, newAdapterTestClock(), "")
	ctx := context.Background()

	seeded, matching, baseCreatedAt := seedAcceptancePaginationCorpus(t, store)

	t.Run("search_returns_exactly_the_seeded_matching_turns", func(t *testing.T) {
		acceptancePaginationAssertSearchHits(ctx, t, adapter, matching)
	})

	t.Run("search_for_an_unseeded_token_returns_zero", func(t *testing.T) {
		acceptancePaginationAssertSearchEmpty(ctx, t, adapter)
	})

	t.Run("pagination_walks_every_seeded_turn_with_no_loss_or_duplication", func(t *testing.T) {
		acceptancePaginationAssertWalk(ctx, t, adapter, seeded)
	})

	t.Run("retention_prunes_only_turns_older_than_the_window", func(t *testing.T) {
		acceptancePaginationAssertRetention(ctx, t, store, db, seeded, baseCreatedAt)
	})
}

// acceptanceFixedClock and fixedClockAt: a Clock whose Now() always
// reports the given unix second -- retention.go's Adapter.PruneTurns
// reads a.clock.Now() at call time, so this is how the test names an
// exact cutoff instant without waiting on the wall clock (Art.7.3: no
// bare time.Now in production code; this is test-only).
type acceptanceFixedClock struct{ unix int64 }

func (c acceptanceFixedClock) Now() time.Time { return time.Unix(c.unix, 0).UTC() }

func fixedClockAt(unixSeconds int64) Clock { return acceptanceFixedClock{unix: unixSeconds} }
