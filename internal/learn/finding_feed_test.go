package learn

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/storage/migrate"
)

type feedEffects struct{}

func (feedEffects) BeginEffect(_ context.Context, req audit.EffectRequest) (audit.EffectHandle, error) {
	return audit.EffectHandle{Key: req.Key, Kind: req.Kind, IntentSeq: 1}, nil
}
func (feedEffects) ConfirmEffect(context.Context, audit.EffectHandle, string) (audit.Record, error) {
	return audit.Record{}, nil
}
func (feedEffects) FailEffect(context.Context, audit.EffectHandle, string) (audit.Record, error) {
	return audit.Record{}, nil
}

func newFeedFixture(t *testing.T) (*sql.DB, *jobs.EvidenceLedger) {
	t.Helper()
	db := openMigratedDB(t, tmplOutcomeJobs)
	ctx := context.Background()
	if err := jobs.ApplyEvidenceSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatal(err)
	}
	// The proposal-store migration belongs to another ticket. Install its
	// contract table here without changing the learn-config migration.
	if _, err := db.Exec(`CREATE TABLE jobs_learn_finding_feed
		(outcome_id INTEGER PRIMARY KEY REFERENCES jobs_telemetry_outcomes(id), fed_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ledger, err := jobs.NewEvidenceLedger(jobs.NewStore(db), newTestClock(), feedEffects{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return db, ledger
}

func seedFeedOutcome(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	ctx := context.Background()
	if err := jobs.NewStore(db).PutJob(ctx, baseJobForReconcile(id)); err != nil {
		t.Fatal(err)
	}
	o := baseOutcome(id)
	o.Component = "generated-code"
	if err := NewSQLiteOutcomeWriter(db, newTestClock()).Record(ctx, o); err != nil {
		t.Fatal(err)
	}
}

func appendFeedEvidence(t *testing.T, ledger *jobs.EvidenceLedger, id string, kind jobs.EvidenceKind,
	outcome jobs.Outcome, producer jobs.ProducerCapability) {
	t.Helper()
	_, err := ledger.Append(context.Background(), jobs.EvidenceRecord{
		JobID: id, Kind: kind, ProducerCapability: producer, Outcome: outcome,
		AttemptID: "attempt-1", CheckpointID: "cp-1", TreeHash: "tree-1",
		AttestorIdentity: "daemon:d-1", ResultHash: "result-1", ArtifactRef: "artifact-1",
		IdempotencyKey: id + "-" + string(kind) + "-" + string(outcome),
	}, jobs.AppendAuthorization{})
	if err != nil {
		t.Fatal(err)
	}
}

// TestFindingFeedWritesToolFailures exercises the real ledger, outcomes and
// SQLite writer, including restart idempotency and atomic failure recovery.
func TestFindingFeedWritesToolFailures(t *testing.T) {
	db, ledger := newFeedFixture(t)
	ctx := context.Background()
	for _, id := range []string{"job-failed", "job-passed", "job-missing"} {
		seedFeedOutcome(t, db, id)
	}
	for _, kind := range []jobs.EvidenceKind{jobs.EvidenceBuild, jobs.EvidenceLint, jobs.EvidenceTests} {
		appendFeedEvidence(t, ledger, "job-failed", kind, jobs.OutcomeFail, jobs.ProducerControllerRun)
	}
	appendFeedEvidence(t, ledger, "job-failed", jobs.EvidenceReview, jobs.OutcomeFail, jobs.ProducerControllerRun)
	appendFeedEvidence(t, ledger, "job-passed", jobs.EvidenceBuild, jobs.OutcomeFail, jobs.ProducerControllerRun)
	appendFeedEvidence(t, ledger, "job-passed", jobs.EvidenceBuild, jobs.OutcomePass, jobs.ProducerControllerRun)
	appendFeedEvidence(t, ledger, "job-passed", jobs.EvidenceLint, jobs.OutcomeFail, jobs.ProducerAgentClaim)
	appendFeedEvidence(t, ledger, "job-passed", jobs.EvidenceTests, jobs.OutcomePending, jobs.ProducerControllerRun)
	feed, err := NewFindingFeed(db, ledger, NewSQLiteFindingWriter(db))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := feed.Run(ctx); err != nil || n != 3 {
		t.Fatalf("Run = %d, %v; want 3", n, err)
	}
	if n := countQuery(t, db, `SELECT COUNT(*) FROM jobs_telemetry_finding f JOIN jobs_telemetry_outcomes o
		ON o.id=f.outcome_id WHERE o.job_id='job-failed' AND f.family='tool_failure'
		AND f.category='crash' AND f.severity='normal' AND f.count=1`); n != 3 {
		t.Fatalf("structured failure rows = %d, want 3", n)
	}
	if n := countQuery(t, db, `SELECT COUNT(*) FROM jobs_telemetry_finding`); n != 3 {
		t.Fatalf("all finding rows = %d, want 3", n)
	}
	if n := countQuery(t, db, `SELECT COUNT(*) FROM jobs_learn_finding_feed`); n != 3 {
		t.Fatalf("feed markers = %d, want 3", n)
	}
	feed, err = NewFindingFeed(db, ledger, NewSQLiteFindingWriter(db))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := feed.Run(ctx); err != nil || n != 0 {
		t.Fatalf("repeat Run = %d, %v; want 0", n, err)
	}
	t.Run("atomic retry", func(t *testing.T) { testFeedRetry(t, db, ledger, feed) })
	t.Run("dependencies", func(t *testing.T) { testFeedDependencies(t, db, ledger) })
}

// testFeedDependencies checks that NewFindingFeed refuses a nil ledger, db or writer.
func testFeedDependencies(t *testing.T, db *sql.DB, ledger *jobs.EvidenceLedger) {
	t.Helper()
	var nilLedger *jobs.EvidenceLedger
	for _, l := range []EvidenceQuerier{nil, nilLedger} {
		if _, err := NewFindingFeed(db, l, NewSQLiteFindingWriter(db)); err == nil {
			t.Fatal("nil ledger accepted")
		}
	}
	if _, err := NewFindingFeed(nil, ledger, NewSQLiteFindingWriter(db)); err == nil {
		t.Fatal("nil db accepted")
	}
	if _, err := NewFindingFeed(db, ledger, (*SQLiteFindingWriter)(nil)); err == nil {
		t.Fatal("nil writer accepted")
	}
}

func testFeedRetry(t *testing.T, db *sql.DB, ledger *jobs.EvidenceLedger, feed *FindingFeed) {
	t.Helper()
	seedFeedOutcome(t, db, "job-retry")
	appendFeedEvidence(t, ledger, "job-retry", jobs.EvidenceBuild, jobs.OutcomeFail, jobs.ProducerControllerRun)
	appendFeedEvidence(t, ledger, "job-retry", jobs.EvidenceLint, jobs.OutcomeFail, jobs.ProducerControllerRun)
	if _, err := db.Exec(`CREATE TRIGGER reject_feed BEFORE INSERT ON jobs_telemetry_finding
		WHEN (SELECT COUNT(*) FROM jobs_telemetry_finding) >= 4
		BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if n, err := feed.Run(context.Background()); err == nil || n != 0 {
		t.Fatalf("failed Run = %d, %v", n, err)
	}
	if countQuery(t, db, `SELECT COUNT(*) FROM jobs_telemetry_finding`) != 3 ||
		countQuery(t, db, `SELECT COUNT(*) FROM jobs_learn_finding_feed`) != 3 {
		t.Fatal("failed transaction left a finding or marker")
	}
	if _, err := db.Exec(`DROP TRIGGER reject_feed`); err != nil {
		t.Fatal(err)
	}
	bad, err := NewFindingFeed(db, feedQueryError{}, NewSQLiteFindingWriter(db))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := bad.Run(context.Background()); err == nil || n != 0 {
		t.Fatalf("ledger error = %d, %v", n, err)
	}
	if n, err := feed.Run(context.Background()); err != nil || n != 2 {
		t.Fatalf("retry = %d, %v; want 2", n, err)
	}
}

type feedQueryError struct{}

func (feedQueryError) Query(context.Context, string, jobs.EvidenceKind) (jobs.EvidenceRecord, error) {
	return jobs.EvidenceRecord{}, errors.New("injected ledger error")
}
