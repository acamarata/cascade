// Purpose: one bad job never wedges a reconcile pass, proven on real
//
//	SQLite: a free-text planner id, credential-shaped and dotted class
//	strings and a client-path scope each still yield exactly one stored
//	row per terminal job; a credential-shaped job id is skipped and
//	counted while every later terminal job records.
//
// SPORT: learn/reconcile-refusal/ADD (P1-CAP-02).
package learn

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// seedBadJobs stores the accepted jobs TestReconcileNeverWedgesOnOneBadJob
// reconciles: each odd input sorts before the valid "job-ok-after".
func seedBadJobs(t *testing.T, db *sql.DB, aws, slack string) *jobs.Store {
	t.Helper()
	ctx := context.Background()
	if err := jobs.ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyJobsSchema: %v", err)
	}
	store := jobs.NewStore(db)
	put := func(id string, edit func(*jobs.Job)) {
		j := baseJobForReconcile(id)
		j.State = jobs.JobStateAccepted
		edit(&j)
		if err := store.PutJob(ctx, j); err != nil {
			t.Fatalf("PutJob: %v", err)
		}
	}
	put("intent:fix the login bug", func(*jobs.Job) {})
	put("a-job-aws-class", func(j *jobs.Job) { j.MinTaskClass = aws })
	put("a-job-slack-class", func(j *jobs.Job) { j.MinTaskClass = slack })
	put("a-job-dotted-risk", func(j *jobs.Job) { j.RiskClass = "a.example.com" })
	put("a-job-client-scope", func(j *jobs.Job) { j.MutableScope = "clients/acme/contract.md" })
	put("a-job-"+aws, func(*jobs.Job) {})
	put("job-ok-after", func(*jobs.Job) {})
	return store
}

// TestReconcileNeverWedgesOnOneBadJob: jobs with unstorable or
// credential-shaped inputs are listed before a valid job; after two passes
// every storable terminal job has exactly one row with sanitised columns,
// the credential-id job has none, and each pass reports the refusal count.
func TestReconcileNeverWedgesOnOneBadJob(t *testing.T) {
	ctx := context.Background()
	canaries := registryCanaries()
	slack, aws := canaries["api-slack"], canaries["api-aws"]
	db := newTestOutcomeDB(t)
	store := seedBadJobs(t, db, aws, slack)

	rec := OutcomeReconciler{Store: store, Writer: NewSQLiteOutcomeWriter(db, newTestClock())}
	for pass := 1; pass <= 2; pass++ {
		err := rec.Reconcile(ctx)
		if err == nil || !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "refused 1") {
			t.Fatalf("pass %d: err = %v, want KindInvalidInput counting 1 refused job", pass, err)
		}
		if strings.Contains(errChainText(err), aws) {
			t.Fatalf("pass %d: the reconcile error carries the canary", pass)
		}
	}
	stored := map[string]string{ // stored job_id -> expected task_class|risk_class|scope_ref
		"job-" + digestID("intent:fix the login bug"): "code|normal|scope-" + digestID("repo:/tmp/x"),
		"a-job-aws-class":    "unknown|normal|scope-" + digestID("repo:/tmp/x"),
		"a-job-slack-class":  "unknown|normal|scope-" + digestID("repo:/tmp/x"),
		"a-job-dotted-risk":  "code|unknown|scope-" + digestID("repo:/tmp/x"),
		"a-job-client-scope": "code|normal|scope-" + digestID("clients/acme/contract.md"),
		"job-ok-after":       "code|normal|scope-" + digestID("repo:/tmp/x"),
	}
	for id, want := range stored {
		var n int
		var got string
		err := db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(task_class||'|'||risk_class||'|'||scope_ref), '') FROM `+
			tableTelemetryOutcomes+` WHERE job_id = ?`, id).Scan(&n, &got)
		if err != nil {
			t.Fatalf("read row for a stored job: %v", err)
		}
		if n != 1 || got != want {
			t.Errorf("job %d-char id: rows=%d columns=%q, want exactly 1 row with %q", len(id), n, got, want)
		}
	}
	if n := countRows(t, db, tableTelemetryOutcomes); n != len(stored) {
		t.Errorf("outcome rows = %d, want %d (one per storable terminal job)", n, len(stored))
	}
	for class, canary := range map[string]string{"api-aws": aws, "api-slack": slack} {
		assertCanaryInNoColumn(t, db, class, canary)
	}
}
