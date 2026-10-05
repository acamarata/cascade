package jobs

// Purpose: UnconfirmedOutbox returns exactly the unconfirmed rows of the
//
//	requested site: the intent and effect rows of that site, none of
//	another site's rows and none of its own confirmed rows. Guarded
//	against an empty result by positive controls on every assertion.
//
// SPORT: jobs/scheduler-outbox/ADD (tests) (P1-CI-01).

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

func newOutboxQueryDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyJobsSchema: %v", err)
	}
	if err := ApplyOutboxSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyOutboxSchema: %v", err)
	}
	if err := NewStore(db).PutJob(ctx, baseJob("q-job")); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	return db
}

// seedQueryRow records one intent for (site, payload) at clock and moves
// it to state; the returned key names the row.
func seedQueryRow(t *testing.T, db *sql.DB, site OutboxSite, payload string, clock int64, state OutboxState) string {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	in := OutboxIntent{JobID: "q-job", Site: site, AttemptGeneration: 1, PayloadHash: payload}
	if _, err := RecordIntent(ctx, tx, clock, in); err != nil {
		t.Fatalf("RecordIntent: %v", err)
	}
	key := DeriveIdempotencyKey(site, in.JobID, in.AttemptGeneration, payload)
	switch state {
	case OutboxEffectState:
		err = MarkEffect(ctx, tx, clock, key)
	case OutboxConfirmed:
		err = ConfirmEffect(ctx, tx, clock, key)
	case OutboxIntentState, OutboxReconciled:
		// RecordIntent already left the row in intent.
	}
	if err != nil {
		t.Fatalf("transition to %s: %v", state, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return key
}

func TestUnconfirmedOutboxBySite(t *testing.T) {
	db := newOutboxQueryDB(t)
	ctx := context.Background()

	wantIntent := seedQueryRow(t, db, OutboxSiteCIDispatch, "p-intent", 10, OutboxIntentState)
	wantEffect := seedQueryRow(t, db, OutboxSiteCIDispatch, "p-effect", 20, OutboxEffectState)
	seedQueryRow(t, db, OutboxSiteCIDispatch, "p-confirmed", 30, OutboxConfirmed)
	seedQueryRow(t, db, OutboxSiteSpawn, "p-other-site", 15, OutboxIntentState)

	got, err := UnconfirmedOutbox(ctx, db, OutboxSiteCIDispatch)
	if err != nil {
		t.Fatalf("UnconfirmedOutbox: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d (%+v), want exactly the 2 unconfirmed ci_dispatch rows", len(got), got)
	}
	if got[0].IdempotencyKey != wantIntent || got[0].State != OutboxIntentState {
		t.Fatalf("first row = %+v, want the oldest (intent) row %q", got[0], wantIntent)
	}
	if got[1].IdempotencyKey != wantEffect || got[1].State != OutboxEffectState {
		t.Fatalf("second row = %+v, want the effect row %q", got[1], wantEffect)
	}
	for _, r := range got {
		if r.Site != OutboxSiteCIDispatch {
			t.Fatalf("row %+v carries site %q, want ci_dispatch only", r, r.Site)
		}
	}

	other, err := UnconfirmedOutbox(ctx, db, OutboxSiteSpawn)
	if err != nil || len(other) != 1 || other[0].PayloadHash != "p-other-site" {
		t.Fatalf("spawn rows = %+v, err %v; want exactly the one spawn row", other, err)
	}
	none, err := UnconfirmedOutbox(ctx, db, OutboxSiteIntegration)
	if err != nil || len(none) != 0 {
		t.Fatalf("integration rows = %+v, err %v; want zero (positive control above proves the reader sees rows)", none, err)
	}
}

func TestUnconfirmedOutboxRefusesBadInput(t *testing.T) {
	db := newOutboxQueryDB(t)
	if _, err := UnconfirmedOutbox(context.Background(), nil, OutboxSiteCIDispatch); err == nil {
		t.Fatal("nil db: want a refusal")
	}
	_, err := UnconfirmedOutbox(context.Background(), db, OutboxSite("nope"))
	if !errors.Is(err, ErrInvalidOutboxSite) {
		t.Fatalf("unknown site: err = %v, want ErrInvalidOutboxSite", err)
	}
}
