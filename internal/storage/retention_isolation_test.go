// Purpose: DomainPruner.Prune's multi-domain isolation, aggregate-error,
//
//	configuration-error, and Clock-required contract — split from
//	retention_test.go per R-14.117 (Art.10.3's 300-line cap; mechanical
//	relocation, no behavior change). Shared helpers live in
//	retention_helpers_test.go.
//
// SPORT: internal.storage.retention.DomainPruner/ADDED (P1-E02-W1-S03-T2).

package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
)

// --- DomainPruner.Prune: multi-domain isolation + aggregate error ------

// TestPrune_MultiDomainIsolation proves Prune only deletes rows from the
// domain(s) it was actually configured to touch, even when a sibling
// domain's table holds equally-old rows.
func TestPrune_MultiDomainIsolation(t *testing.T) {
	db := openRetentionTestDB(t)
	createTSTable(t, db, "context_domain_root")
	createTSTable(t, db, "memory_domain_root")

	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	clock := testkit.NewFrozenClock(now)
	old := now.Add(-48 * time.Hour).Unix()
	insertTSRow(t, db, "context_domain_root", old, "old-context")
	insertTSRow(t, db, "memory_domain_root", old, "old-memory")

	cfg := RetentionConfig{
		DomainRetention: map[DomainID]time.Duration{DomainContext: time.Hour},
		Targets: map[DomainID][]PruneTarget{
			DomainContext: {{Table: "context_domain_root", TimestampColumn: "ts"}},
		},
		Clock: clock,
	}
	if _, err := (DomainPruner{}).Prune(context.Background(), db, cfg); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if got := countRows(t, db, "context_domain_root"); got != 0 {
		t.Errorf("context_domain_root rows = %d, want 0", got)
	}
	if got := countRows(t, db, "memory_domain_root"); got != 1 {
		t.Errorf("memory_domain_root rows = %d, want 1 (untargeted domain must be untouched)", got)
	}
}

// TestPrune_AggregateError proves a per-domain failure (here: a domain
// configured with a target table that does not exist, standing in for a
// write-path failure — this ticket's files_scope has no way to fake the
// write-executor itself, so a real DB-level error is the honest
// equivalent) does not cancel sibling domains: the sibling's real prune
// still happens and the joined error names the failing domain.
func TestPrune_AggregateError(t *testing.T) {
	db := openRetentionTestDB(t)
	createTSTable(t, db, "memory_domain_root")

	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	clock := testkit.NewFrozenClock(now)
	old := now.Add(-48 * time.Hour).Unix()
	insertTSRow(t, db, "memory_domain_root", old, "old-memory")

	cfg := RetentionConfig{
		DomainRetention: map[DomainID]time.Duration{
			DomainContext: time.Hour, // target table below does not exist
			DomainMemory:  time.Hour,
		},
		Targets: map[DomainID][]PruneTarget{
			DomainContext: {{Table: "context_domain_root_missing", TimestampColumn: "ts"}},
			DomainMemory:  {{Table: "memory_domain_root", TimestampColumn: "ts"}},
		},
		Clock: clock,
	}
	reports, err := (DomainPruner{}).Prune(context.Background(), db, cfg)
	if err == nil {
		t.Fatal("Prune err = nil, want a joined error naming the failing domain")
	}
	if !strings.Contains(err.Error(), string(DomainContext)) {
		t.Errorf("joined error %q does not name the failing domain %s", err.Error(), DomainContext)
	}

	ctxReport := findReport(t, reports, DomainContext)
	if ctxReport.Err == nil {
		t.Error("DomainContext report.Err = nil, want the DELETE failure")
	}

	memReport := findReport(t, reports, DomainMemory)
	if memReport.Err != nil || memReport.RowsDeleted != 1 {
		t.Errorf("DomainMemory report = %+v, want RowsDeleted=1, Err=nil (sibling must still be pruned)", memReport)
	}
	if got := countRows(t, db, "memory_domain_root"); got != 0 {
		t.Errorf("memory_domain_root rows = %d, want 0", got)
	}
}

// TestPrune_ConfigErrorWhenTargetMissing proves a non-zero retention
// window with no registered PruneTarget is a reported configuration
// error, never a silent no-op that pretends the domain was handled.
func TestPrune_ConfigErrorWhenTargetMissing(t *testing.T) {
	db := openRetentionTestDB(t)
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))

	cfg := RetentionConfig{
		DomainRetention: map[DomainID]time.Duration{DomainJobs: time.Hour},
		Clock:           clock,
	}
	reports, err := (DomainPruner{}).Prune(context.Background(), db, cfg)
	if err == nil {
		t.Fatal("Prune err = nil, want a configuration error for the target-less domain")
	}
	r := findReport(t, reports, DomainJobs)
	if r.Err == nil {
		t.Error("DomainJobs report.Err = nil, want a configuration error")
	}
}

// --- Clock-required error paths -----------------------------------------

func TestPrune_RequiresClock(t *testing.T) {
	db := openRetentionTestDB(t)
	if _, err := (DomainPruner{}).Prune(context.Background(), db, RetentionConfig{}); err == nil {
		t.Error("Prune with nil Clock: err = nil, want KindInvalidInput")
	}
}

func TestVacuumJob_RequiresClock(t *testing.T) {
	db := openRetentionTestDB(t)
	if _, err := (VacuumJob{}).Run(context.Background(), db); err == nil {
		t.Error("VacuumJob.Run with nil Clock: err = nil, want KindInvalidInput")
	}
}
