// Purpose: UpsertRunSourceStream and RunViaStream (domain_source.go): the
// stream marker is written with the source row in one transaction, and an
// absent marker reads as not-via-stream. Split from domain_source_test.go
// to keep both files under the 300-line cap.
//
// SPORT: internal.ci.UpsertRunSourceStream/TESTED,
//
//	internal.ci.RunViaStream/TESTED (P1-CI-01).
package ci

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestUpsertRunSourceLocalStream proves UpsertRunSourceStream writes the
// ci_run_source row and the stream marker together, idempotently, and that
// a corrupt tier or source is refused with nothing written.
func TestUpsertRunSourceLocalStream(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	if err := UpsertRunSourceStream(ctx, db, -5, 9, SourceLocal, true, provider.SensitivityLocalOnly, "ckpt-1"); err != nil {
		t.Fatalf("UpsertRunSourceStream: %v", err)
	}
	if err := UpsertRunSourceStream(ctx, db, -5, 9, SourceLocal, true, provider.SensitivityLocalOnly, "ckpt-1"); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if src, err := runSource(ctx, db, -5, 9); err != nil || src != SourceLocal {
		t.Fatalf("runSource = %q, %v; want local", src, err)
	}
	var via int
	var tier, ckpt string
	if err := db.QueryRow(`SELECT via_stream, sensitivity, checkpoint_id FROM ci_run_stream WHERE run_id=-5 AND repo_id=9`).Scan(&via, &tier, &ckpt); err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if via != 1 || tier != "local-only" || ckpt != "ckpt-1" {
		t.Fatalf("marker = %d %q %q, want 1 local-only ckpt-1", via, tier, ckpt)
	}
	if on, err := RunViaStream(ctx, db, -5, 9); err != nil || !on {
		t.Fatalf("RunViaStream = %v, %v; want true", on, err)
	}
	if err := UpsertRunSourceStream(ctx, db, -6, 9, SourceLocal, true, provider.SensitivityTier(77), "x"); err == nil {
		t.Fatal("an invalid tier must be refused")
	}
	if err := UpsertRunSourceStream(ctx, db, -6, 9, "nonsense", true, provider.SensitivityInternal, "x"); err == nil {
		t.Fatal("an unknown source must be refused")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ci_run_source WHERE run_id=-6`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a refused upsert wrote %d rows (err %v), want 0", n, err)
	}
}

// TestRunStreamAbsentIsFalse proves a run with no stream marker reads as
// not-via-stream: UpsertRunSource (what `cascade ci run` and the poller
// use) never writes one, and an unknown run reads false too.
func TestRunStreamAbsentIsFalse(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	if err := UpsertRunSource(ctx, db, -7, 9, SourceLocal); err != nil {
		t.Fatalf("UpsertRunSource: %v", err)
	}
	if err := UpsertRunSourceStream(ctx, db, -8, 9, SourceLocal, true, provider.SensitivityInternal, "c"); err != nil {
		t.Fatalf("UpsertRunSourceStream: %v", err)
	}
	if on, err := RunViaStream(ctx, db, -8, 9); err != nil || !on {
		t.Fatalf("positive control: RunViaStream(stream run) = %v, %v; want true", on, err)
	}
	for _, id := range []int64{-7, -404} {
		if on, err := RunViaStream(ctx, db, id, 9); err != nil || on {
			t.Fatalf("RunViaStream(%d) = %v, %v; want false (absent marker)", id, on, err)
		}
	}
	if err := UpsertRunSource(ctx, db, -8, 9, SourceLocal); err != nil {
		t.Fatalf("UpsertRunSource over a stream run: %v", err)
	}
	if on, err := RunViaStream(ctx, db, -8, 9); err != nil || !on {
		t.Fatalf("UpsertRunSource must not clear the stream marker: %v, %v", on, err)
	}
}
