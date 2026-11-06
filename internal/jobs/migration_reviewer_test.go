package jobs

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestApplyReviewerSchemaIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	db := openTestDB(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := ApplyReviewerSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
			t.Fatalf("apply #%d: %v", i+1, err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tableReviewerDecision).Scan(&n); err != nil {
		t.Fatalf("count tables: %v", err)
	}
	if n != 1 {
		t.Fatalf("jobs_reviewer_decision tables = %d, want 1", n)
	}
	if set := ReviewerMigrationSet(); set.SetID != "jobs-reviewer" || set.SchemaVersion != 1 || set.ReaderCeiling != 1 {
		t.Fatalf("set = %s v%d ceiling %d", set.SetID, set.SchemaVersion, set.ReaderCeiling)
	}
	if err := ApplyReviewerSchema(ctx, nil, migrate.SQLiteEmitter{}, newTestClock(), "", ""); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil db = %v, want KindInvalidInput", err)
	}
	if err := ApplyReviewerSchema(ctx, db, migrate.SQLiteEmitter{}, nil, "", ""); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil clock = %v, want KindInvalidInput", err)
	}
}
