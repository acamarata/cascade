// Purpose: DomainPruner.Prune's boundary/no-op/idempotency/batch-cap
//
//	contract — split from retention_test.go per R-14.117 (Art.10.3's
//	300-line cap; mechanical relocation, no behavior change). Shared
//	helpers live in retention_helpers_test.go.
//
// SPORT: internal.storage.retention.DomainPruner/ADDED (P1-E02-W1-S03-T2).

package storage

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
)

// --- DomainPruner.Prune: boundary semantics -----------------------------

// TestPrune_BoundarySemantics proves the exact cutoff comparison: a row
// strictly older than the window is deleted, a row exactly AT the cutoff
// (age == window, not > window) survives, and a row younger than the
// cutoff survives. This is the off-by-one case the ticket calls out by
// name.
func TestPrune_BoundarySemantics(t *testing.T) {
	db := openRetentionTestDB(t)
	createTSTable(t, db, "context_domain_root")

	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	clock := testkit.NewFrozenClock(now)
	window := 24 * time.Hour
	cutoff := now.Add(-window).Unix()

	insertTSRow(t, db, "context_domain_root", cutoff-1000, "well-older")   // deleted
	insertTSRow(t, db, "context_domain_root", cutoff-1, "just-older")      // deleted
	insertTSRow(t, db, "context_domain_root", cutoff, "exactly-at-cutoff") // survives
	insertTSRow(t, db, "context_domain_root", cutoff+1, "just-newer")      // survives

	cfg := RetentionConfig{
		DomainRetention: map[DomainID]time.Duration{DomainContext: window},
		Targets: map[DomainID][]PruneTarget{
			DomainContext: {{Table: "context_domain_root", TimestampColumn: "ts"}},
		},
		Clock: clock,
	}
	reports, err := (DomainPruner{}).Prune(context.Background(), db, cfg)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	report := findReport(t, reports, DomainContext)
	if report.RowsDeleted != 2 {
		t.Errorf("RowsDeleted = %d, want 2 (only the two rows strictly older than the cutoff)", report.RowsDeleted)
	}
	if got := countRows(t, db, "context_domain_root"); got != 2 {
		t.Errorf("remaining rows = %d, want 2 (cutoff row + newer row must survive)", got)
	}

	var fillers []string
	rows, err := db.Query(`SELECT filler FROM context_domain_root ORDER BY ts`)
	if err != nil {
		t.Fatalf("query survivors: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			t.Fatalf("scan: %v", err)
		}
		fillers = append(fillers, f)
	}
	want := []string{"exactly-at-cutoff", "just-newer"}
	if len(fillers) != 2 || fillers[0] != want[0] || fillers[1] != want[1] {
		t.Errorf("survivors = %v, want %v", fillers, want)
	}
}

// --- DomainPruner.Prune: no-op / idempotency ----------------------------

// TestPruneNoOp proves a zero (unset) retention window is a true no-op:
// zero rows deleted, nil error, and the row that WOULD have qualified
// (well past any plausible window) is left untouched — no DELETE is ever
// issued for this domain.
func TestPruneNoOp(t *testing.T) {
	db := openRetentionTestDB(t)
	createTSTable(t, db, "audit_domain_root")

	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	clock := testkit.NewFrozenClock(now)
	insertTSRow(t, db, "audit_domain_root", now.Add(-10000*time.Hour).Unix(), "ancient")

	cfg := RetentionConfig{
		// DomainAudit intentionally absent from DomainRetention: the map
		// default (0) IS the no-op case, no explicit zero-value entry
		// needed.
		Targets: map[DomainID][]PruneTarget{
			DomainAudit: {{Table: "audit_domain_root", TimestampColumn: "ts"}},
		},
		Clock: clock,
	}
	reports, err := (DomainPruner{}).Prune(context.Background(), db, cfg)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	report := findReport(t, reports, DomainAudit)
	if report.RowsDeleted != 0 || report.Err != nil {
		t.Errorf("report = %+v, want RowsDeleted=0, Err=nil", report)
	}
	if got := countRows(t, db, "audit_domain_root"); got != 1 {
		t.Errorf("rows = %d, want 1 (no-op must not delete the ancient row)", got)
	}
}

// TestPruneIdempotent proves a second Prune call against an
// already-pruned domain deletes 0 rows and returns nil, exiting cleanly
// (§5.9).
func TestPruneIdempotent(t *testing.T) {
	db := openRetentionTestDB(t)
	createTSTable(t, db, "secrets_domain_root")

	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	clock := testkit.NewFrozenClock(now)
	window := time.Hour
	insertTSRow(t, db, "secrets_domain_root", now.Add(-2*time.Hour).Unix(), "stale")
	insertTSRow(t, db, "secrets_domain_root", now.Add(-1*time.Minute).Unix(), "fresh")

	cfg := RetentionConfig{
		DomainRetention: map[DomainID]time.Duration{DomainSecrets: window},
		Targets: map[DomainID][]PruneTarget{
			DomainSecrets: {{Table: "secrets_domain_root", TimestampColumn: "ts"}},
		},
		Clock: clock,
	}

	first, err := (DomainPruner{}).Prune(context.Background(), db, cfg)
	if err != nil {
		t.Fatalf("first Prune: %v", err)
	}
	if r := findReport(t, first, DomainSecrets); r.RowsDeleted != 1 {
		t.Fatalf("first Prune RowsDeleted = %d, want 1", r.RowsDeleted)
	}

	second, err := (DomainPruner{}).Prune(context.Background(), db, cfg)
	if err != nil {
		t.Fatalf("second Prune: %v", err)
	}
	r := findReport(t, second, DomainSecrets)
	if r.RowsDeleted != 0 || r.Err != nil {
		t.Errorf("second Prune report = %+v, want RowsDeleted=0, Err=nil", r)
	}
	if got := countRows(t, db, "secrets_domain_root"); got != 1 {
		t.Errorf("rows = %d, want 1 (the fresh row, untouched by either call)", got)
	}
}

// --- DomainPruner.Prune: batch cap (direct dbExecer instrumentation) ---

// countingExecer wraps a real *sql.DB, counting every ExecContext call
// (deleteOlderThan issues exactly one ExecContext per DELETE round-trip,
// and one QueryRowContext for its COUNT) so the test can assert the exact
// round-trip count against the real engine underneath — never a mock of
// the engine itself (Art.2), only an observability shim over the one seam
// (dbExecer) this package already factors out for this purpose.
type countingExecer struct {
	db      *sql.DB
	deletes int
}

func (c *countingExecer) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.HasPrefix(strings.TrimSpace(query), "DELETE") {
		c.deletes++
	}
	return c.db.ExecContext(ctx, query, args...)
}

func (c *countingExecer) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return c.db.QueryRowContext(ctx, query, args...)
}

// TestPruneBatchCap inserts 1500 rows and prunes with cap 500, asserting
// exactly three DELETE round-trips (never one large DELETE, never a
// spurious fourth confirming round) and that all 1500 rows are gone.
func TestPruneBatchCap(t *testing.T) {
	db := openRetentionTestDB(t)
	createTSTable(t, db, "queue_domain_root")

	const rowCount = 1500
	old := int64(1000)
	for i := 0; i < rowCount; i++ {
		insertTSRow(t, db, "queue_domain_root", old, "batch")
	}

	ce := &countingExecer{db: db}
	target := PruneTarget{Table: "queue_domain_root", TimestampColumn: "ts"}
	n, err := deleteOlderThan(context.Background(), ce, target, old+1, 500)
	if err != nil {
		t.Fatalf("deleteOlderThan: %v", err)
	}
	if n != rowCount {
		t.Errorf("rows deleted = %d, want %d", n, rowCount)
	}
	if ce.deletes != 3 {
		t.Errorf("DELETE round-trips = %d, want 3 (ceil(1500/500))", ce.deletes)
	}
	if got := countRows(t, db, "queue_domain_root"); got != 0 {
		t.Errorf("remaining rows = %d, want 0", got)
	}
}

// TestPruneBatchCap_EmptyTarget proves a target with nothing to delete
// issues zero DELETE round-trips (only the COUNT query), not one wasted
// empty DELETE.
func TestPruneBatchCap_EmptyTarget(t *testing.T) {
	db := openRetentionTestDB(t)
	createTSTable(t, db, "jobs_domain_root")

	ce := &countingExecer{db: db}
	target := PruneTarget{Table: "jobs_domain_root", TimestampColumn: "ts"}
	// time.Now() here is fine: this is a _test.go file (forbidigo and the
	// internal/build clockgate AST gate both exempt tests, Art.7.3), and
	// the exact cutoff value is arbitrary — the table is empty, so no
	// row's age is ever compared against it.
	n, err := deleteOlderThan(context.Background(), ce, target, time.Now().Unix(), 500)
	if err != nil {
		t.Fatalf("deleteOlderThan: %v", err)
	}
	if n != 0 {
		t.Errorf("rows deleted = %d, want 0", n)
	}
	if ce.deletes != 0 {
		t.Errorf("DELETE round-trips = %d, want 0 (empty table, COUNT alone proves nothing to delete)", ce.deletes)
	}
}
