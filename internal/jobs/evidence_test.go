package jobs

// Purpose: EvidenceLedger's own contract -- Append happy path, Query
//
//	latest, audit.Writer call count, and rows surviving a store reopen
//	(the S-60.T4 resume guarantee) -- over a real modernc-sqlite db in
//	t.TempDir() (Art.2/Art.7.1).
//
// SPORT: jobs/completion-gate/ADD (P1-E29-W6-S60-T3).

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/runtime"
)

// recordingWriter counts audit.Writer.Append calls and captures them.
type recordingWriter struct {
	calls []audit.Event
	err   error
}

func (w *recordingWriter) Append(_ context.Context, e audit.Event) (audit.Record, error) {
	w.calls = append(w.calls, e)
	return audit.Record{}, w.err
}

// alwaysAuthorized is a ProducerAuthz stand-in that permits every Append,
// for tests that only care about the ledger's own append-only mechanics.
func alwaysAuthorized(t *testing.T, store *Store) *ProducerAuthz {
	t.Helper()
	if err := store.PutExecution(context.Background(), Execution{ID: "exec-1", JobID: "job-1", State: ExecutionRunning}); err != nil {
		t.Fatalf("seed execution: %v", err)
	}
	return NewProducerAuthz(store, func() bool { return true }, nil)
}

func baseAuth() AppendAuthorization {
	return AppendAuthorization{ExecutionID: "exec-1"}
}

func newLedgerFixture(t *testing.T, now time.Time) (*EvidenceLedger, *Store, *recordingWriter) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "evidence.db")
	db, err := openEvidenceDB(t, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store := NewStore(db)
	if err := store.PutJob(context.Background(), baseJob("job-1")); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	authz := alwaysAuthorized(t, store)
	clock := runtime.NewFixedClock(now)
	w := &recordingWriter{}
	l, err := NewEvidenceLedger(store, clock, w, authz)
	if err != nil {
		t.Fatalf("NewEvidenceLedger: %v", err)
	}
	return l, store, w
}

func passRecord(kind EvidenceKind, idem string) EvidenceRecord {
	return EvidenceRecord{
		JobID: "job-1", Kind: kind, ProducerCapability: ProducerControllerRun,
		AttemptID: "attempt-1", CheckpointID: "cp-1", TreeHash: "tree-1",
		AttestorIdentity: "daemon:d-1", Outcome: OutcomePass, ResultHash: "res-1",
		IdempotencyKey: idem, ArtifactRef: "artifact-1",
	}
}

func TestEvidenceAppendHappyPath(t *testing.T) {
	l, _, w := newLedgerFixture(t, time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	rec, err := l.Append(ctx, passRecord(EvidenceBuild, "idem-1"), baseAuth())
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if rec.Seq != 1 {
		t.Fatalf("Seq = %d, want 1", rec.Seq)
	}
	if len(w.calls) != 1 {
		t.Fatalf("audit calls = %d, want 1", len(w.calls))
	}
}

func TestEvidenceQueryLatest(t *testing.T) {
	l, _, _ := newLedgerFixture(t, time.Now())
	ctx := context.Background()
	if _, err := l.Append(ctx, passRecord(EvidenceBuild, "idem-1"), baseAuth()); err != nil {
		t.Fatalf("Append 1: %v", err)
	}
	second := passRecord(EvidenceBuild, "idem-2")
	second.Outcome = OutcomeFail
	if _, err := l.Append(ctx, second, baseAuth()); err != nil {
		t.Fatalf("Append 2: %v", err)
	}
	rec, err := l.Query(ctx, "job-1", EvidenceBuild)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if rec.Outcome != OutcomeFail || rec.Seq != 2 {
		t.Fatalf("Query = %+v, want the second (seq 2, fail) row", rec)
	}
	if _, err := l.Query(ctx, "job-1", EvidenceLint); err != ErrNoEvidence {
		t.Fatalf("Query(lint) err = %v, want ErrNoEvidence", err)
	}
}

func TestEvidenceKindProducerOutcomeValid(t *testing.T) {
	if EvidenceKind("bogus").Valid() {
		t.Error("EvidenceKind(bogus).Valid() = true, want false")
	}
	if ProducerCapability("bogus").Valid() {
		t.Error("ProducerCapability(bogus).Valid() = true, want false")
	}
	if Outcome("bogus").Valid() {
		t.Error("Outcome(bogus).Valid() = true, want false")
	}
}

func TestNewEvidenceLedgerRequiresCollaborators(t *testing.T) {
	store := &Store{}
	clock := runtime.NewFixedClock(time.Now())
	writer := &recordingWriter{}
	if _, err := NewEvidenceLedger(nil, clock, writer, nil); err == nil {
		t.Error("NewEvidenceLedger with no store = nil error, want a refusal")
	}
	if _, err := NewEvidenceLedger(store, nil, writer, nil); err == nil {
		t.Error("NewEvidenceLedger with no clock = nil error, want a refusal")
	}
	if _, err := NewEvidenceLedger(store, clock, nil, nil); err == nil {
		t.Error("NewEvidenceLedger with no writer = nil error, want a refusal")
	}
}

func TestEvidenceAppendValidatesFields(t *testing.T) {
	l, _, _ := newLedgerFixture(t, time.Now())
	ctx := context.Background()
	cases := map[string]EvidenceRecord{
		"no job id":          {Kind: EvidenceBuild, ProducerCapability: ProducerControllerRun, Outcome: OutcomePass, IdempotencyKey: "k"},
		"unknown kind":       {JobID: "job-1", Kind: "bogus", ProducerCapability: ProducerControllerRun, Outcome: OutcomePass, IdempotencyKey: "k"},
		"unknown capability": {JobID: "job-1", Kind: EvidenceBuild, ProducerCapability: "bogus", Outcome: OutcomePass, IdempotencyKey: "k"},
		"unknown outcome":    {JobID: "job-1", Kind: EvidenceBuild, ProducerCapability: ProducerControllerRun, Outcome: "bogus", IdempotencyKey: "k"},
		"no idempotency key": {JobID: "job-1", Kind: EvidenceBuild, ProducerCapability: ProducerControllerRun, Outcome: OutcomePass},
	}
	for name, rec := range cases {
		if _, err := l.Append(ctx, rec, baseAuth()); err == nil {
			t.Errorf("Append(%s) = nil error, want a refusal", name)
		}
	}
}

func TestEvidenceRowsSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resume.db")
	db, err := openEvidenceDB(t, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store := NewStore(db)
	if err := store.PutJob(context.Background(), baseJob("job-1")); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	authz := alwaysAuthorized(t, store)
	clock := runtime.NewFixedClock(time.Now())
	l, err := NewEvidenceLedger(store, clock, &recordingWriter{}, authz)
	if err != nil {
		t.Fatalf("NewEvidenceLedger: %v", err)
	}
	ctx := context.Background()
	if _, err := l.Append(ctx, passRecord(EvidenceBuild, "idem-1"), baseAuth()); err != nil {
		t.Fatalf("Append: %v", err)
	}
	_ = db.Close()

	db2, err := openEvidenceDBExisting(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = db2.Close() }()
	store2 := NewStore(db2)
	l2, err := NewEvidenceLedger(store2, clock, &recordingWriter{}, nil)
	if err != nil {
		t.Fatalf("NewEvidenceLedger reopened: %v", err)
	}
	cursor, err := l2.Cursor(ctx, "job-1")
	if err != nil {
		t.Fatalf("Cursor after reopen: %v", err)
	}
	if cursor != 1 {
		t.Fatalf("Cursor after reopen = %d, want 1", cursor)
	}
}
