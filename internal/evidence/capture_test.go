// Purpose: Capture/Pin/Referenced against real counterparts: a real
// providers/fs.BlobStore over t.TempDir() and a real internal/jobs.Store
// wired through a tiny in-test ArtifactWriter adapter (see capture.go's
// IMPORT-CYCLE note for why evidence itself never imports internal/jobs).
// package evidence_test (external) so this file, alone in the package, can
// import both internal/evidence and internal/jobs without risking the
// import cycle capture.go's own doc comment documents.
//
// SPORT: evidence/capture-pin (ADD).
package evidence_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/evidence"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/fs"

	_ "modernc.org/sqlite"
)

var fixedCaptureTime = time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)

func sqlOpen(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// jobsArtifactAdapter satisfies evidence.ArtifactWriter over a real
// *jobs.Store -- the one-line adapter capture.go's doc comment names as
// the composition root's job.
type jobsArtifactAdapter struct{ store *jobs.Store }

func (a jobsArtifactAdapter) PutArtifact(ctx context.Context, artifactID, jobID, executionID, blobKey string, class evidence.DataClass) error {
	return a.store.PutArtifact(ctx, jobs.Artifact{
		ID: artifactID, JobID: jobID, ExecutionID: executionID, Kind: "evidence",
		BlobKey: blobKey, ConsequenceClass: jobs.ConsequenceNormal, DataClass: jobs.DataClass(class),
	})
}

func newCapturer(t *testing.T) (*evidence.Capturer, *evidence.Store, string, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "capture-test.db")
	db, err := sqlOpen(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := testkit.NewFrozenClock(fixedCaptureTime)
	ctx := context.Background()
	if err := jobs.ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyJobsSchema: %v", err)
	}
	if err := evidence.ApplySchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	jobStore := jobs.NewStore(db)
	if err := jobStore.PutJob(ctx, jobs.Job{
		ID: "job-1", State: jobs.JobStatePending, CreatedAt: 1, UpdatedAt: 1,
		Capabilities: []string{"code"}, MutableScope: "repo:/tmp/x", RiskClass: "normal",
		MinTaskClass: "code", NodeRequirements: "{}", TimeoutSeconds: 60, CostCeiling: 1.0, Priority: 1,
		ConsequenceClass: jobs.ConsequenceNormal, DataClass: jobs.DataClassInternal,
	}); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	if err := jobStore.PutExecution(ctx, jobs.Execution{ID: "exec-1", JobID: "job-1", State: jobs.ExecutionRunning}); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}

	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatalf("fs.New: %v", err)
	}
	store := evidence.NewStore(db, clock)
	capturer := evidence.NewCapturer(blobs, jobsArtifactAdapter{store: jobStore}, store)
	return capturer, store, "job-1", "exec-1"
}

func TestCaptureIdempotent(t *testing.T) {
	c, _, jobID, execID := newCapturer(t)
	ctx := context.Background()
	data := []byte("observed byte range")

	ref1, hash1, err := c.Capture(ctx, jobID, execID, evidence.DataClassInternal, data)
	if err != nil {
		t.Fatalf("Capture (1st): %v", err)
	}
	ref2, hash2, err := c.Capture(ctx, jobID, execID, evidence.DataClassInternal, data)
	if err != nil {
		t.Fatalf("Capture (2nd): %v", err)
	}
	if ref1 != ref2 {
		t.Errorf("Capture not idempotent: %q vs %q", ref1, ref2)
	}
	if hash1 != hash2 {
		t.Errorf("Capture hash not stable: %q vs %q", hash1, hash2)
	}
	if hash1 != evidence.ContentHash(data) {
		t.Errorf("Capture hash = %q, want %q", hash1, evidence.ContentHash(data))
	}
}

func TestCaptureRejectsMissingScope(t *testing.T) {
	c, _, _, _ := newCapturer(t)
	if _, _, err := c.Capture(context.Background(), "", "exec-1", evidence.DataClassInternal, []byte("x")); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Capture(empty jobID) = %v, want typed invalid-input", err)
	}
}

func TestPinAndReferenced(t *testing.T) {
	c, store, jobID, execID := newCapturer(t)
	ctx := context.Background()

	ref, hash, err := c.Capture(ctx, jobID, execID, evidence.DataClassInternal, []byte("pinned bytes"))
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if err := store.PutClaim(ctx, evidence.Claim{
		ID: "CLM-PIN", Statement: "s", Type: evidence.ClaimObservedFact, Confidence: 1,
		ProducedBy: evidence.ProducedBy{RunID: "run-1"}, DataClass: evidence.DataClassInternal,
	}); err != nil {
		t.Fatalf("PutClaim: %v", err)
	}
	if err := store.PutEvidence(ctx, evidence.Evidence{
		ID: "EVD-PIN", ClaimID: "CLM-PIN",
		Source:      evidence.Source{Type: evidence.SourceArtifact, Locator: evidence.Locator{Kind: evidence.LocatorArtifact, ArtifactRef: ref}},
		ContentHash: hash, DataClass: evidence.DataClassInternal, CapturedRef: ref,
	}); err != nil {
		t.Fatalf("PutEvidence: %v", err)
	}
	if err := c.Pin(ctx, "CLM-PIN"); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if err := c.Pin(ctx, "CLM-MISSING"); err != evidence.ErrClaimNotFound {
		t.Fatalf("Pin(missing claim) = %v, want ErrClaimNotFound", err)
	}

	referenced, err := c.Referenced(ctx, ref)
	if err != nil {
		t.Fatalf("Referenced: %v", err)
	}
	if !referenced {
		t.Error("Referenced(pinned artifact) = false, want true")
	}
	unreferenced, err := c.Referenced(ctx, "artifact://ART-nope")
	if err != nil {
		t.Fatalf("Referenced (unreferenced): %v", err)
	}
	if unreferenced {
		t.Error("Referenced(unknown artifact) = true, want false")
	}
}
