package ci

import (
	"context"
	"crypto/rand"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

type localAttestRig struct {
	a       *LocalAttestor
	store   *jobs.CiAttestationStore
	nonces  *jobs.AttestationNonceStore
	ref     JobRef
	res     AcceptanceResult
	nonce   string
	expires time.Time
}

func newLocalAttestRig(t *testing.T) localAttestRig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	ctx, clock := context.Background(), newTestClock()
	db := openFileDB(t, "attest.db")
	for _, err := range []error{ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""), jobs.ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", "")} {
		if err != nil {
			t.Fatal(err)
		}
	}
	self, key, err := nodes.GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ks, err := nodes.NewNodeKeystore(secrets.Config{Dir: home, ForceFileVault: true, Service: "local-attest-test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ks.Store(ctx, self.NodeID, key); err != nil {
		t.Fatal(err)
	}
	store, err := jobs.NewCiAttestationStore(db)
	if err != nil {
		t.Fatal(err)
	}
	nonces, err := jobs.NewAttestationNonceStore(db, clock)
	if err != nil {
		t.Fatal(err)
	}
	nonce, expires, err := nonces.Issue(ctx, "job", "checkpoint", "registry-id", "spec-at-issue", "policy-at-issue", []string{"unit"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewLocalAttestor(self, func(p []byte) ([]byte, error) { return ks.Sign(ctx, self.NodeID, p) }, store, attest.NewControllerTrustStore(self, nil), func() string { return "wrong-current-policy" }, clock)
	if err != nil {
		t.Fatal(err)
	}
	seedLocalAttestRun(t, db)
	return localAttestRig{a: a, store: store, nonces: nonces, ref: JobRef{JobID: "job", CheckpointCommit: "commit"}, nonce: nonce, expires: expires,
		res: AcceptanceResult{CheckpointID: "checkpoint", TreeHash: "tree", Passed: true, Results: []CIResultEvent{{JobID: "job", CheckpointID: "checkpoint", TreeHash: "tree", Kind: RequirementUnit, ExecutorKind: "local", RunID: -91, RepoID: 7, Passed: true}}}}
}

func seedLocalAttestRun(t *testing.T, db *sql.DB) {
	t.Helper()
	clock := newTestClock()
	run := sampleRun(-91)
	run.RepoID = 7
	run.HeadSHA = "commit"
	run.Status = RunStatusCompleted
	run.Conclusion = ConclusionSuccess
	job := Job{JobID: -91, RunID: -91, Name: "unit", Status: RunStatusCompleted, Conclusion: ConclusionSuccess, StartedAt: clock.Now(), FinishedAt: clock.Now()}
	if err := Upsert(context.Background(), db, run, []Job{job}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestLocalExecutorAttestationVerifies(t *testing.T) {
	r := newLocalAttestRig(t)
	id, err := r.a.AttestRerun(context.Background(), r.res, r.ref, r.nonce, r.expires)
	if err != nil {
		t.Fatal(err)
	}
	row, ok, err := r.store.ByID(context.Background(), id)
	if err != nil || !ok || row.AttestorClass != string(attest.ClassEnrolledNode) {
		t.Fatalf("row: %v %v %v", row, ok, err)
	}
	sa := attest.SignedAttestation{CanonicalJSON: []byte(row.CanonicalJSON), Signature: row.Signature}
	if err := attest.VerifySignature(context.Background(), sa, "commit", "tree", r.a.trust, r.a.clock.Now()); err != nil {
		t.Fatal(err)
	}
	t.Run("custody failure stores no row", func(t *testing.T) {
		r := newLocalAttestRig(t)
		failure := cascade.New(cascade.KindUnavailable, "identity backend unavailable")
		r.a.sign = func([]byte) ([]byte, error) { return nil, failure }
		if _, err := r.a.AttestRerun(context.Background(), r.res, r.ref, r.nonce, r.expires); err == nil || !strings.Contains(err.Error(), failure.Error()) {
			t.Fatalf("error: %v", err)
		}
		rows, err := r.store.ByCommitSHA(context.Background(), "commit")
		if err != nil || len(rows) != 0 {
			t.Fatalf("unsigned row: %v %v", rows, err)
		}
	})
	t.Run("node executor refused", func(t *testing.T) {
		r := newLocalAttestRig(t)
		r.res.Results[0].ExecutorKind = "node"
		if _, err := r.a.AttestRerun(context.Background(), r.res, r.ref, r.nonce, r.expires); err == nil {
			t.Fatal("signed node executor with controller key")
		}
	})
}

func TestAttestRerunCopiesFactFields(t *testing.T) {
	r := newLocalAttestRig(t)
	id, err := r.a.AttestRerun(context.Background(), r.res, r.ref, r.nonce, r.expires)
	if err != nil {
		t.Fatal(err)
	}
	row, ok, err := r.store.ByID(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("row %v %v", ok, err)
	}
	fact, ok, err := r.nonces.Issued(context.Background(), r.nonce)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if row.RepoID != fact.RepoID || row.JobSpecHash != fact.JobSpecHash || row.PolicyVersion != fact.PolicyVersion || row.CheckpointID != fact.CheckpointID {
		t.Fatalf("stored record lost issued facts: %v %v", row, fact)
	}
	t.Run("plan changed after issue", func(t *testing.T) {
		r := newLocalAttestRig(t)
		r.res.Results[0].Kind = RequirementLint
		if _, err := r.a.AttestRerun(context.Background(), r.res, r.ref, r.nonce, r.expires); err != ErrCheckpointStale {
			t.Fatalf("changed plan: %v", err)
		}
	})
}
