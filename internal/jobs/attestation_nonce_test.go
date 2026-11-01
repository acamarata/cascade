package jobs

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/testkit"
)

func newNonceStore(t *testing.T) (*AttestationNonceStore, *sql.DB, *testkit.FrozenClock) {
	t.Helper()
	db := newTestStore(t).db
	clock := testkit.NewFrozenClock(attestStart)
	st, err := NewAttestationNonceStore(db, clock)
	if err != nil {
		t.Fatal(err)
	}
	return st, db, clock
}

// consume runs ConsumeOnAcceptTx in its own transaction.
func consume(t *testing.T, db *sql.DB, st *AttestationNonceStore, nonce, job, commit, tree string, now time.Time) error {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ConsumeOnAcceptTx(context.Background(), tx, nonce, job, commit, tree, now); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func TestAttestationNonceStore(t *testing.T) {
	st, db, _ := newNonceStore(t)
	ctx := context.Background()
	commit, tree := strings.Repeat("a1", 20), strings.Repeat("b2", 20)
	seen := map[string]bool{}
	for range 8 {
		n, _, err := st.Issue(ctx, "job-1", "ckpt-1", "repo-a", "spec", "policy-v1", []string{"unit"}, time.Hour)
		if err != nil || len(n) != 32 || strings.Trim(n, "0123456789abcdef") != "" || seen[n] {
			t.Fatalf("Issue = %q, %v; want a fresh 128-bit hex nonce", n, err)
		}
		seen[n] = true
	}
	nonce, expires, err := st.Issue(ctx, "job-1", "ckpt-1", "repo-a", "spec", "policy-v1", []string{"unit", "lint"}, 72*time.Hour)
	if err != nil || !expires.Equal(attestStart.Add(24*time.Hour)) {
		t.Fatalf("Issue(72 h) expires %v, %v; want capped at 24 h", expires, err)
	}
	if _, ok, err := st.AcceptedFor(ctx, nonce, commit, tree, "ckpt-1"); ok || err != nil {
		t.Fatalf("AcceptedFor before consume = %v, %v; want not accepted", ok, err)
	}
	if err := consume(t, db, st, nonce, "job-2", commit, tree, attestStart.Add(time.Hour)); err == nil {
		t.Fatal("ConsumeOnAcceptTx(another job) = nil")
	}
	if err := consume(t, db, st, nonce, "job-1", commit, tree, attestStart.Add(time.Hour)); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if err := consume(t, db, st, nonce, "job-1", commit, tree, attestStart.Add(2*time.Hour)); !sentinelOnChain(err, attest.ErrNonceReused) {
		t.Fatalf("second consume = %v, want ErrNonceReused", err)
	}
	fact, ok, err := st.AcceptedFor(ctx, nonce, commit, tree, "ckpt-1")
	if err != nil || !ok || fact.Commit != commit || fact.Tree != tree || !fact.AcceptedAt.Equal(attestStart.Add(time.Hour)) ||
		!slices.Equal(fact.PlanKinds, []string{"lint", "unit"}) {
		t.Fatalf("AcceptedFor = %+v, %v, %v", fact, ok, err)
	}
	for name, args := range map[string][3]string{
		"other commit": {strings.Repeat("c3", 20), tree, "ckpt-1"}, "other tree": {commit, strings.Repeat("d4", 20), "ckpt-1"},
		"other checkpoint": {commit, tree, "ckpt-2"},
	} {
		if _, ok, _ := st.AcceptedFor(ctx, nonce, args[0], args[1], args[2]); ok {
			t.Errorf("AcceptedFor(%s) = accepted", name)
		}
	}
	late, _, _ := st.Issue(ctx, "job-1", "ckpt-1", "repo-a", "spec", "policy-v1", []string{"unit"}, time.Minute)
	if err := consume(t, db, st, late, "job-1", commit, tree, attestStart.Add(time.Minute)); !sentinelOnChain(err, attest.ErrExpiredAttestation) {
		t.Fatalf("consume at expiry = %v, want ErrExpiredAttestation", err)
	}
	if err := consume(t, db, st, "unknown", "job-1", commit, tree, attestStart); err == nil {
		t.Fatal("consume(unknown nonce) = nil")
	}
}

func TestAttestationNonceStoreRefusesBadInput(t *testing.T) {
	st, _, _ := newNonceStore(t)
	ctx := context.Background()
	bad := map[string]func() error{
		"no job":     func() error { _, _, err := st.Issue(ctx, "", "c", "r", "s", "p", []string{"k"}, time.Hour); return err },
		"no repo":    func() error { _, _, err := st.Issue(ctx, "j", "c", "", "s", "p", []string{"k"}, time.Hour); return err },
		"no kinds":   func() error { _, _, err := st.Issue(ctx, "j", "c", "r", "s", "p", nil, time.Hour); return err },
		"empty kind": func() error { _, _, err := st.Issue(ctx, "j", "c", "r", "s", "p", []string{""}, time.Hour); return err },
		"kind with ':'": func() error {
			_, _, err := st.Issue(ctx, "j", "c", "r", "s", "p", []string{"a:b"}, time.Hour)
			return err
		},
		"zero ttl":      func() error { _, _, err := st.Issue(ctx, "j", "c", "r", "s", "p", []string{"k"}, 0); return err },
		"nil tx":        func() error { return st.ConsumeOnAcceptTx(ctx, nil, "n", "j", "c", "t", attestStart) },
		"nil db or clk": func() error { _, err := NewAttestationNonceStore(nil, nil); return err },
	}
	for name, call := range bad {
		if call() == nil {
			t.Errorf("%s: nil error, want a refusal", name)
		}
	}
	if _, ok, err := st.Issued(ctx, "missing"); ok || err != nil {
		t.Fatalf("Issued(missing) = %v, %v", ok, err)
	}
}

func TestAttestationNonceStoresBinding(t *testing.T) {
	st, db, _ := newNonceStore(t)
	ctx := context.Background()
	nonce, _, err := st.Issue(ctx, "job-7", "ckpt-7", "repo-registry-7", "spec-7", "policy-v7", []string{"unit", "lint"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	want := AcceptanceFact{Nonce: nonce, JobID: "job-7", CheckpointID: "ckpt-7", RepoID: "repo-registry-7",
		JobSpecHash: "spec-7", PolicyVersion: "policy-v7", PlanKinds: []string{"lint", "unit"}}
	issued, ok, err := st.Issued(ctx, nonce)
	if err != nil || !ok || !factEqual(issued, want) {
		t.Fatalf("Issued = %+v, %v, %v; want %+v", issued, ok, err, want)
	}
	commit, tree := strings.Repeat("a1", 20), strings.Repeat("b2", 20)
	if err := consume(t, db, st, nonce, "job-7", commit, tree, attestStart.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	fact, ok, err := st.AcceptedFor(ctx, nonce, commit, tree, "ckpt-7")
	want.Commit, want.Tree, want.AcceptedAt = commit, tree, attestStart.Add(time.Minute)
	if err != nil || !ok || !factEqual(fact, want) {
		t.Fatalf("AcceptedFor = %+v, %v, %v; want %+v", fact, ok, err, want)
	}
	if issued, _, _ := st.Issued(ctx, nonce); issued.Commit != "" || issued.RepoID != "repo-registry-7" {
		t.Fatalf("Issued after acceptance = %+v; want the Issue-time binding only", issued)
	}
}

func factEqual(a, b AcceptanceFact) bool {
	return a.Nonce == b.Nonce && a.JobID == b.JobID && a.CheckpointID == b.CheckpointID && a.RepoID == b.RepoID &&
		a.JobSpecHash == b.JobSpecHash && a.PolicyVersion == b.PolicyVersion && a.Commit == b.Commit &&
		a.Tree == b.Tree && slices.Equal(a.PlanKinds, b.PlanKinds) && a.AcceptedAt.Equal(b.AcceptedAt)
}
