// Purpose: usageMigrationSet/ApplyUsageMigrationSchema's own tests: table
//
//	presence via sqlite_master, idempotent re-apply on both an empty and a
//	pre-migrated db, and UpdateOutcomeClass's not-found contract - all
//	against a REAL modernc-sqlite database file under t.TempDir() (Art.2:
//	a real counterpart, never an in-memory self-authored double), mirroring
//	internal/jobs/migration_test.go's own fixture shape.
//
// SPORT: conductor.usage-attribution/ADD (P1-E11-W3-S23-T4).
package conductor

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver for this package's tests

	"github.com/acamarata/cascade/internal/providers/usage"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// usageFakeClock is a fixed migrate.Clock for tests - no bare time.Now
// (Art.7.3).
type usageFakeClock struct{ t time.Time }

func (c usageFakeClock) Now() time.Time { return c.t }

// newUsageTestClock returns the concrete usageFakeClock type (not an
// interface) so callers can pass it wherever ANY structurally-identical
// Clock is expected (migrate.Clock, internal/providers/usage.Clock,
// internal/runtime.Clock all declare exactly Now() time.Time).
func newUsageTestClock() usageFakeClock {
	return usageFakeClock{t: time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)}
}

// openUsageTestDB opens a REAL modernc-sqlite database file under
// t.TempDir().
func openUsageTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conductor-usage-test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newUsageTestStore opens a fresh real db, applies the jobs_usage
// migration, and returns a UsageStore over it.
func newUsageTestStore(t *testing.T) *UsageStore {
	t.Helper()
	db := openUsageTestDB(t)
	if err := ApplyUsageMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, newUsageTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyUsageMigrationSchema: %v", err)
	}
	return NewUsageStore(db)
}

// newUsageAggregatorTestManager opens a fresh REAL modernc-sqlite database
// and returns a real *usage.Manager over it, migrated via J/S-20.T4's own
// ApplyMigrationSchema - the real aggregate counterpart, not a hand-rolled
// double, for tests that need to assert the aggregate row actually landed.
func newUsageAggregatorTestManager(t *testing.T) *usage.Manager {
	t.Helper()
	db := openUsageTestDB(t)
	clk := newUsageTestClock()
	if err := usage.ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clk, "", ""); err != nil {
		t.Fatalf("usage.ApplyMigrationSchema: %v", err)
	}
	return usage.NewManager(db, clk)
}

// fakeCostEstimator is a fixed-cost CostEstimator double.
type fakeCostEstimator struct{ cost int64 }

func (f fakeCostEstimator) EstimateCostMicroUSD(_, _ string, _, _ int64) int64 { return f.cost }

// spyUsageStore is an erroring/recording UsageRecorder double, used only
// where a test needs to inject a write failure - every test that can use
// the real *UsageStore does (usage_test.go's own header comment).
type spyUsageStore struct {
	mu   sync.Mutex
	rows []UsageRecord
	err  error
}

func (s *spyUsageStore) WriteUsageRecord(_ context.Context, r UsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.rows = append(s.rows, r)
	return nil
}

// spyUsageAggregator is an erroring/recording UsageAggregator double, used
// only where a test needs to inject a failure or inspect the exact
// IncrementRequest built (TestUsage_ProviderError's Error=true assertion).
type spyUsageAggregator struct {
	mu    sync.Mutex
	calls []usage.IncrementRequest
	err   error
}

func (a *spyUsageAggregator) IncrementUsage(_ context.Context, req usage.IncrementRequest) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	a.calls = append(a.calls, req)
	return nil
}

func (a *spyUsageAggregator) snapshot() []usage.IncrementRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]usage.IncrementRequest, len(a.calls))
	copy(out, a.calls)
	return out
}

// TestUsageMigration_Idempotent asserts both re-apply cases task 3 names:
// an empty db's first apply, and a second apply against the now-migrated
// db - both must exit cleanly, and the table must actually exist
// afterward (sqlite_master, not merely "no error").
func TestUsageMigration_Idempotent(t *testing.T) {
	db := openUsageTestDB(t)
	ctx := context.Background()

	if err := ApplyUsageMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newUsageTestClock(), "", ""); err != nil {
		t.Fatalf("first apply (empty db): %v", err)
	}
	if err := ApplyUsageMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newUsageTestClock(), "", ""); err != nil {
		t.Fatalf("second apply (pre-migrated db, idempotent re-apply): %v", err)
	}

	var name string
	if err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tableUsage).
		Scan(&name); err != nil {
		t.Fatalf("jobs_usage table missing after apply: %v", err)
	}
	if name != tableUsage {
		t.Fatalf("table name = %q, want %q", name, tableUsage)
	}
}

func TestUsageMigration_NilArgsRefuse(t *testing.T) {
	ctx := context.Background()
	if err := ApplyUsageMigrationSchema(ctx, nil, migrate.SQLiteEmitter{}, newUsageTestClock(), "", ""); err == nil {
		t.Error("ApplyUsageMigrationSchema(nil db) = nil, want error")
	}
	db := openUsageTestDB(t)
	if err := ApplyUsageMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, nil, "", ""); err == nil {
		t.Error("ApplyUsageMigrationSchema(nil clock) = nil, want error")
	}
}

// TestUsageMigration_UpdateOutcomeClass_UnknownJobID asserts the typed
// not-found error AND that no row is written on the refused path (task 3).
func TestUsageMigration_UpdateOutcomeClass_UnknownJobID(t *testing.T) {
	store := newUsageTestStore(t)
	ctx := context.Background()

	err := store.UpdateOutcomeClass(ctx, JobID("does-not-exist"), "accepted")
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("UpdateOutcomeClass(unknown job_id) = %v, want KindNotFound", err)
	}

	var n int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+tableUsage).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 0 {
		t.Fatalf("jobs_usage row count = %d, want 0 (not-found path must write nothing)", n)
	}
}

// TestUsageMigration_WriteThenUpdateOutcomeClass proves WriteUsageRecord
// persists a row and UpdateOutcomeClass mutates ONLY that row's
// outcome_class column, asserted by re-reading the PERSISTED ROW.
func TestUsageMigration_WriteThenUpdateOutcomeClass(t *testing.T) {
	store := newUsageTestStore(t)
	ctx := context.Background()
	rec := UsageRecord{
		JobID: JobID("job-1"), LaneID: "lane-1", TaskClass: "chat",
		TokensIn: 10, TokensOut: 20, CostMicroUSD: 5, WallMS: 100,
		OutcomeClass: outcomeUnknown, Attempt: 1, RequestingEntity: "standalone",
	}
	if err := store.WriteUsageRecord(ctx, rec); err != nil {
		t.Fatalf("WriteUsageRecord: %v", err)
	}
	if err := store.UpdateOutcomeClass(ctx, rec.JobID, "accepted"); err != nil {
		t.Fatalf("UpdateOutcomeClass: %v", err)
	}

	var laneID, outcome string
	if err := store.db.QueryRowContext(ctx, `SELECT lane_id, outcome_class FROM `+tableUsage+` WHERE job_id = ?`,
		string(rec.JobID)).Scan(&laneID, &outcome); err != nil {
		t.Fatalf("select persisted row: %v", err)
	}
	if laneID != rec.LaneID {
		t.Errorf("lane_id = %q, want %q (UpdateOutcomeClass must not touch other columns)", laneID, rec.LaneID)
	}
	if outcome != "accepted" {
		t.Fatalf("outcome_class = %q, want %q", outcome, "accepted")
	}
}
