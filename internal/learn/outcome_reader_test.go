package learn

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestSQLiteOutcomeReaderWindow checks join deduplication and inclusive endpoints.
func TestSQLiteOutcomeReaderWindow(t *testing.T) {
	db := newTestOutcomeDB(t)
	now := newTestClock().Now()
	seedPatternOutcome(t, db, now.Add(-Window), 0, "a", "generated-code")
	seedPatternOutcome(t, db, now, 1, "b", "generated-code")
	seedPatternOutcome(t, db, now.Add(time.Second), 2, "c", "generated-code")
	rows, err := NewSQLiteOutcomeReader(db).ReadWindow(context.Background(), now.Add(-Window), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d outcomes, want 2", len(rows))
	}
	for _, row := range rows {
		if len(row.Findings) != 2 || row.Findings[0].JobID != row.JobID {
			t.Fatalf("wrong join: %+v", row)
		}
	}
	if !rows[0].CompletedAt.Equal(now.Add(-Window)) || !rows[1].CompletedAt.Equal(now) {
		t.Fatal("timestamp mapping")
	}
}

// TestSQLiteOutcomeReaderRefuses checks unavailable and malformed storage.
func TestSQLiteOutcomeReaderRefuses(t *testing.T) {
	ctx := context.Background()
	now := newTestClock().Now()
	db := newTestOutcomeDB(t)
	r := NewSQLiteOutcomeReader(db)
	seedPatternOutcome(t, db, now, 0, "a", "generated-code")
	if _, err := db.Exec(`UPDATE jobs_telemetry_finding SET category = 'unknown'`); err != nil {
		t.Fatal(err)
	}
	rows, err := r.ReadWindow(ctx, now.Add(-Window), now)
	if !cascade.HasKind(err, cascade.KindInvalidInput) || rows != nil {
		t.Fatalf("malformed finding accepted: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadWindow(ctx, now.Add(-Window), now); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("closed database: %v", err)
	}
	if _, err := NewSQLiteOutcomeReader(nil).ReadWindow(ctx, now.Add(-Window), now); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil database: %v", err)
	}
}
