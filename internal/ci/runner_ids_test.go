// Purpose: ReserveLocalRun/LocalRepoID tests against a live in-memory
// SQLite database -- including the exact failure the clock-derived
// generator had: two runs allocated under an injected FIXED clock must
// still be two distinct rows.
// SPORT: internal.ci.ReserveLocalRun/TESTED, internal.ci.LocalRepoID/TESTED
//
//	(P1-E25-W5-S51-T5).
package ci

import (
	"context"
	"database/sql"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

// reserveTestDB opens a migrated in-memory database for the id tests.
func reserveTestDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	return ctx, db
}

// TestReserveLocalRun_TwoRunsUnderAFixedClock is the regression: the old
// NewLocalRunID returned clock.Now().UnixNano(), so two runs under
// NewFixedClock collided on one id and the second silently upserted over
// the first. Allocation through the store cannot do that.
func TestReserveLocalRun_TwoRunsUnderAFixedClock(t *testing.T) {
	ctx, db := reserveTestDB(t)
	clock := newTestClock()
	repoID := LocalRepoID("/repo/a")

	first, err := ReserveLocalRun(ctx, db, repoID, "a", clock.Now())
	if err != nil {
		t.Fatalf("ReserveLocalRun (first): %v", err)
	}
	second, err := ReserveLocalRun(ctx, db, repoID, "a", clock.Now())
	if err != nil {
		t.Fatalf("ReserveLocalRun (second): %v", err)
	}
	if first == second {
		t.Fatalf("two runs under one fixed clock both got id %d", first)
	}
	assertRowCount(t, db, tableRun, 2)
}

// TestReserveLocalRun_AllocatesInTheNegativeNamespace proves the
// documented namespace: local ids are negative, so they can never collide
// with a GitHub Actions run id (always positive), and they count downward
// from -1.
func TestReserveLocalRun_AllocatesInTheNegativeNamespace(t *testing.T) {
	ctx, db := reserveTestDB(t)
	repoID := LocalRepoID("/repo/a")

	first, err := ReserveLocalRun(ctx, db, repoID, "a", newTestClock().Now())
	if err != nil {
		t.Fatalf("ReserveLocalRun: %v", err)
	}
	if first != firstLocalRunID {
		t.Errorf("first local run id = %d, want %d", first, firstLocalRunID)
	}
	second, err := ReserveLocalRun(ctx, db, repoID, "a", newTestClock().Now())
	if err != nil {
		t.Fatalf("ReserveLocalRun: %v", err)
	}
	if second != firstLocalRunID-1 {
		t.Errorf("second local run id = %d, want %d", second, firstLocalRunID-1)
	}
}

// TestReserveLocalRun_IgnoresActionsRows proves a repository with hosted
// Actions history still allocates from -1: the sequence reads only the
// negative half, so a positive Actions id never shifts it.
func TestReserveLocalRun_IgnoresActionsRows(t *testing.T) {
	ctx, db := reserveTestDB(t)
	gh := sampleRun(90210)
	if err := Upsert(ctx, db, gh, nil, nil); err != nil {
		t.Fatalf("Upsert (actions row): %v", err)
	}
	got, err := ReserveLocalRun(ctx, db, gh.RepoID, gh.Name, newTestClock().Now())
	if err != nil {
		t.Fatalf("ReserveLocalRun: %v", err)
	}
	if got != firstLocalRunID {
		t.Errorf("local run id = %d, want %d (an Actions run id must not move the local sequence)", got, firstLocalRunID)
	}
}

// TestReserveLocalRun_PerRepoSequences proves two checkouts keep separate
// sequences: both start at -1, which is what makes (run_id, repo_id) the
// real key rather than run_id alone.
func TestReserveLocalRun_PerRepoSequences(t *testing.T) {
	ctx, db := reserveTestDB(t)
	a, err := ReserveLocalRun(ctx, db, LocalRepoID("/repo/a"), "a", newTestClock().Now())
	if err != nil {
		t.Fatalf("ReserveLocalRun(a): %v", err)
	}
	b, err := ReserveLocalRun(ctx, db, LocalRepoID("/repo/b"), "b", newTestClock().Now())
	if err != nil {
		t.Fatalf("ReserveLocalRun(b): %v", err)
	}
	if a != firstLocalRunID || b != firstLocalRunID {
		t.Errorf("per-repo first ids = (%d, %d), want both %d", a, b, firstLocalRunID)
	}
}

func TestReserveLocalRun_RequiresDB(t *testing.T) {
	if _, err := ReserveLocalRun(context.Background(), nil, 1, "x", newTestClock().Now()); err == nil {
		t.Fatal("expected an error for a nil db")
	}
}

func TestLocalRepoID_StableAndDistinct(t *testing.T) {
	a1 := LocalRepoID("/repo/a")
	a2 := LocalRepoID("/repo/a")
	b := LocalRepoID("/repo/b")
	if a1 != a2 {
		t.Errorf("LocalRepoID(\"/repo/a\") = %d and %d, want the same value for the same path", a1, a2)
	}
	if a1 == b {
		t.Errorf("LocalRepoID(\"/repo/a\") == LocalRepoID(\"/repo/b\") == %d, want distinct values", a1)
	}
}
