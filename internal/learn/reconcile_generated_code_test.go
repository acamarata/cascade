package learn

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
)

type fakeProvenance struct {
	by  map[string]ToolProvenance
	err error
}

func (f fakeProvenance) ToolProvenance(_ context.Context, id string) (ToolProvenance, bool, error) {
	p, ok := f.by[id]
	return p, ok, f.err
}

func componentOf(t *testing.T, db *sql.DB, jobID string) string {
	t.Helper()
	var c string
	if err := db.QueryRow(`SELECT component FROM jobs_telemetry_outcomes WHERE job_id = ?`, jobID).Scan(&c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestReconcileGeneratedCodeProvenance(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDB(t, tmplOutcomeJobs)
	store := jobs.NewStore(db)
	for _, id := range []string{"gen-ok", "gen-unverified", "gen-model-label", "gen-absent"} {
		j := baseJobForReconcile(id)
		j.State = jobs.JobStateFailed
		if err := store.PutJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	prov := fakeProvenance{by: map[string]ToolProvenance{
		"gen-ok":          {Component: "generated-code", Authenticated: true},
		"gen-unverified":  {Component: "generated-code", Authenticated: false},
		"gen-model-label": {Component: "model said so", Authenticated: true},
	}}
	rec := OutcomeReconciler{Store: store, Writer: NewSQLiteOutcomeWriter(db, newTestClock()), Provenance: prov}
	if err := rec.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"gen-ok": "generated-code", "gen-unverified": "unknown",
		"gen-model-label": "unknown", "gen-absent": "unknown"}
	for id, c := range want {
		if got := componentOf(t, db, id); got != c {
			t.Errorf("%s component = %q, want %q", id, got, c)
		}
	}
}

func TestReconcileProvenanceReadErrorIsReturned(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDB(t, tmplOutcomeJobs)
	store := jobs.NewStore(db)
	j := baseJobForReconcile("gen-err")
	j.State = jobs.JobStateFailed
	if err := store.PutJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("provenance read failed")
	rec := OutcomeReconciler{Store: store, Writer: NewSQLiteOutcomeWriter(db, newTestClock()), Provenance: fakeProvenance{err: boom}}
	if err := rec.Reconcile(ctx); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the read error", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM jobs_telemetry_outcomes`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("outcome rows = %d, %v; want 0", n, err)
	}
}
