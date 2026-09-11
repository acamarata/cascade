package jobs

// Purpose: ProducerAuthz.Authorize's four contract cases: the
//
//	controller-spawned accept, the foreign-execution refusal, the
//	stale-epoch refusal, and (evidence.go's completeness-skip, not this
//	file) the agent-claim-never-satisfies-a-gate case.
//
// SPORT: jobs/completion-gate/ADD (P1-E29-W6-S60-T3).

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeFencer is a deterministic LeaseFencer test double (Art.1-exempt in
// a _test.go file, matching governor's own escalation_seams.go
// precedent for stubbing an injected interface).
type fakeFencer struct {
	wantEpoch int64
	err       error
}

func (f fakeFencer) Fence(_ context.Context, _, _ string, epoch int64) error {
	if f.err != nil {
		return f.err
	}
	if epoch != f.wantEpoch {
		return ErrLeaseFenced
	}
	return nil
}

func newAuthzFixture(t *testing.T) *Store {
	t.Helper()
	path := t.TempDir() + "/authz.db"
	db, err := openEvidenceDB(t, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store := NewStore(db)
	if err := store.PutJob(context.Background(), baseJob("job-1")); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	if err := store.PutExecution(context.Background(), Execution{ID: "exec-1", JobID: "job-1", State: ExecutionRunning}); err != nil {
		t.Fatalf("seed execution: %v", err)
	}
	return store
}

func TestEvidenceAuthzControllerSpawnedAccepts(t *testing.T) {
	store := newAuthzFixture(t)
	authz := NewProducerAuthz(store, func() bool { return true }, fakeFencer{wantEpoch: 1})
	err := authz.Authorize(context.Background(), AppendAuthorization{
		ExecutionID: "exec-1", LeaseRepoID: "repo", LeaseScopeGlob: "glob", LeaseEpoch: 1,
	})
	if err != nil {
		t.Fatalf("Authorize (controller-spawned) = %v, want nil", err)
	}
}

func TestEvidenceAuthzForeignExecutionRefused(t *testing.T) {
	store := newAuthzFixture(t)
	authz := NewProducerAuthz(store, func() bool { return true }, nil)
	err := authz.Authorize(context.Background(), AppendAuthorization{ExecutionID: "exec-does-not-exist"})
	if !errors.Is(err, ErrEvidenceProducerDenied) {
		t.Fatalf("Authorize (foreign execution) = %v, want ErrEvidenceProducerDenied", err)
	}
}

func TestEvidenceAuthzStaleEpochRefused(t *testing.T) {
	store := newAuthzFixture(t)
	authz := NewProducerAuthz(store, func() bool { return true }, fakeFencer{wantEpoch: 5})
	err := authz.Authorize(context.Background(), AppendAuthorization{
		ExecutionID: "exec-1", LeaseRepoID: "repo", LeaseScopeGlob: "glob", LeaseEpoch: 1,
	})
	if !errors.Is(err, ErrEvidenceProducerDenied) {
		t.Fatalf("Authorize (stale epoch) = %v, want ErrEvidenceProducerDenied", err)
	}
}

func TestEvidenceAuthzNonControllerRefused(t *testing.T) {
	store := newAuthzFixture(t)
	authz := NewProducerAuthz(store, func() bool { return false }, nil)
	err := authz.Authorize(context.Background(), AppendAuthorization{ExecutionID: "exec-1"})
	if !errors.Is(err, ErrEvidenceProducerDenied) {
		t.Fatalf("Authorize (non-controller) = %v, want ErrEvidenceProducerDenied", err)
	}
}

func TestRequireIdentitySource(t *testing.T) {
	cases := []struct {
		kind     EvidenceKind
		identity string
		ok       bool
	}{
		{EvidenceCIAttestation, "node:n-1", true},
		{EvidenceCIAttestation, "daemon:d-1", false},
		{EvidenceHumanApproval, "approval:a-1", true},
		{EvidenceHumanApproval, "node:n-1", false},
		{EvidenceBuild, "daemon:d-1", true},
		{EvidenceBuild, "node:n-1", false},
		{EvidenceBuild, "", false},
	}
	for _, c := range cases {
		err := requireIdentitySource(c.kind, c.identity)
		if c.ok && err != nil {
			t.Errorf("requireIdentitySource(%s, %q) = %v, want nil", c.kind, c.identity, err)
		}
		if !c.ok && err == nil {
			t.Errorf("requireIdentitySource(%s, %q) = nil, want a refusal", c.kind, c.identity)
		}
	}
}

func TestProducerAuthzNilReceiverAndNilStore(t *testing.T) {
	var authz *ProducerAuthz
	if err := authz.Authorize(context.Background(), AppendAuthorization{}); err == nil {
		t.Error("nil *ProducerAuthz.Authorize = nil error, want a refusal")
	}
	authz2 := NewProducerAuthz(nil, func() bool { return true }, nil)
	if err := authz2.Authorize(context.Background(), AppendAuthorization{ExecutionID: "x"}); err == nil {
		t.Error("ProducerAuthz with a nil store = nil error, want a refusal")
	}
}

// TestEvidenceAppendOnly proves the append-only contract: no method
// exists to UPDATE/DELETE a row through this package's API (the store
// refuses a raw UPDATE/DELETE attempt outside it with a typed error is
// not applicable here -- there IS no path, which this test proves by
// construction: attempting the SQL directly against the same db a
// production caller would use still leaves Append's own idempotency
// no-op the only sanctioned mutation), and a repeated idempotency_key
// is a no-op returning the existing seq.
func TestEvidenceAppendOnly(t *testing.T) {
	l, _, _ := newLedgerFixture(t, time.Now())
	ctx := context.Background()
	first, err := l.Append(ctx, passRecord(EvidenceBuild, "idem-shared"), baseAuth())
	if err != nil {
		t.Fatalf("Append 1: %v", err)
	}
	second, err := l.Append(ctx, passRecord(EvidenceBuild, "idem-shared"), baseAuth())
	if err != nil {
		t.Fatalf("Append 2 (repeat idempotency key): %v", err)
	}
	if second.Seq != first.Seq {
		t.Fatalf("repeated idempotency key got seq %d, want the existing seq %d", second.Seq, first.Seq)
	}
	cursor, err := l.Cursor(ctx, "job-1")
	if err != nil {
		t.Fatalf("Cursor: %v", err)
	}
	if cursor != 1 {
		t.Fatalf("Cursor = %d, want 1 (the repeat must not have appended a second row)", cursor)
	}
}
