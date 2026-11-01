package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// store signs a record of repoID through custody and inserts its row.
func (f *attestFixture) store(t *testing.T, db *sql.DB, repoID, nonce, checkpoint string, exit int) jobs.CiAttestationRow {
	t.Helper()
	return f.storeWith(t, db, repoID, nonce, checkpoint, func(r *attest.AttestationRecord) { r.ExitStatus = exit })
}

// storeWith is store with mutate applied to the record before signing.
func (f *attestFixture) storeWith(t *testing.T, db *sql.DB, repoID, nonce, checkpoint string, mutate func(*attest.AttestationRecord)) jobs.CiAttestationRow {
	t.Helper()
	r := attest.AttestationRecord{
		SchemaVersion: 1, AttestorClass: attest.ClassEnrolledNode, CheckpointID: checkpoint, CommitSHA: f.commit,
		TreeHash: f.tree, StartedAt: f.start, EndedAt: f.start.Add(time.Minute), ExpiresAt: f.start.Add(12 * time.Hour),
		JobSpecHash: "spec-1", KeyID: nodes.KeyIDFor(f.node.PubKey), NodeID: f.node.NodeID,
		Nonce: nonce, PolicyVersion: "policy-v1", RepoID: repoID, TargetSelection: "all",
		ResultHashes: []string{"lint:" + strings.Repeat("1", 64), "unit:" + strings.Repeat("2", 64)},
	}
	mutate(&r)
	sa, err := attest.Attest(r, func(p []byte) ([]byte, error) { return f.keys.Sign(context.Background(), f.node.NodeID, p) })
	if err != nil {
		t.Fatal(err)
	}
	row := jobs.CiAttestationRow{
		ID: attest.RowID(sa), NodeID: r.NodeID, RepoID: r.RepoID, CommitSHA: r.CommitSHA, TreeHash: r.TreeHash,
		JobSpecHash: r.JobSpecHash, StartedAt: r.StartedAt.Unix(), EndedAt: r.EndedAt.Unix(), ExitStatus: r.ExitStatus,
		Signature: sa.Signature, CanonicalJSON: string(sa.CanonicalJSON), PolicyVersion: r.PolicyVersion,
		KeyID: r.KeyID, AttestorClass: string(attest.ClassEnrolledNode), CheckpointID: r.CheckpointID,
		TargetSelection: r.TargetSelection, Nonce: r.Nonce, ExpiresAt: r.ExpiresAt.Unix(), CreatedAt: f.start.Unix(),
	}
	st, _ := jobs.NewCiAttestationStore(db)
	if err := st.Insert(context.Background(), row); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	return row
}

// acceptWith issues and consumes a nonce with an explicit job spec hash and
// policy version.
func (f *attestFixture) acceptWith(t *testing.T, db *sql.DB, checkpoint, spec, policy string, at time.Time) string {
	t.Helper()
	nonces, _ := jobs.NewAttestationNonceStore(db, testkit.NewFrozenClock(f.start))
	ctx := context.Background()
	nonce, _, err := nonces.Issue(ctx, "job-1", checkpoint, "repo-a", spec, policy, []string{"lint", "unit"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tx, _ := db.BeginTx(ctx, nil)
	if err := nonces.ConsumeOnAcceptTx(ctx, tx, nonce, "job-1", f.commit, f.tree, at); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return nonce
}

// TestCiAttestVerifyFactBinding: an accepted, correctly signed, otherwise
// passing record is not eligible when its job spec hash, policy version or
// plan coverage disagrees with the accepted fact, or when it was accepted
// outside its validity window.
func TestCiAttestVerifyFactBinding(t *testing.T) {
	cases := map[string]struct {
		spec, policy string
		at           time.Duration
		mutate       func(*attest.AttestationRecord)
	}{
		"job spec hash differs":  {"spec-other", "policy-v1", time.Minute, func(*attest.AttestationRecord) {}},
		"policy version differs": {"spec-1", "policy-v2", time.Minute, func(*attest.AttestationRecord) {}},
		"plan kind missing":      {"spec-1", "policy-v1", time.Minute, func(r *attest.AttestationRecord) { r.ResultHashes = r.ResultHashes[:1] }},
		"accepted before start":  {"spec-1", "policy-v1", -time.Minute, func(*attest.AttestationRecord) {}},
		"accepted after expiry": {"spec-1", "policy-v1", 40 * time.Minute, func(r *attest.AttestationRecord) {
			r.ExpiresAt = r.StartedAt.Add(30 * time.Minute)
		}},
		"control: all match": {"spec-1", "policy-v1", time.Minute, func(*attest.AttestationRecord) {}},
	}
	for name, c := range cases {
		f := newAttestFixture(t)
		db := f.openDB(t)
		nonce := f.acceptWith(t, db, "ckpt-1", c.spec, c.policy, f.start.Add(c.at))
		f.storeWith(t, db, "repo-a", nonce, "ckpt-1", c.mutate)
		_ = db.Close()
		err := f.runInProcess(t, f.commit, "--require", "--repo-id", "repo-a")
		if want := map[bool]int{true: 0, false: 1}[strings.HasPrefix(name, "control")]; cascade.ExitCode(err) != want {
			t.Errorf("%s: %v (exit %d), want exit %d", name, err, cascade.ExitCode(err), want)
		}
		if err != nil && (!strings.Contains(err.Error(), "no eligible passing attestation") || strings.Contains(err.Error(), "does not verify")) {
			t.Errorf("%s: %v, want an eligibility refusal of a record that verifies", name, err)
		}
	}
}

// TestCiAttestVerifyIgnoresReplaceRefs: a refs/replace entry that swaps the
// commit for one with another tree must not let a record of that other tree
// stand in for the commit's real tree.
func TestCiAttestVerifyIgnoresReplaceRefs(t *testing.T) {
	f := newAttestFixture(t)
	if err := os.WriteFile(filepath.Join(f.repo, "b.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, f.repo, "add", "b.txt")
	gitT(t, f.repo, "commit", "-q", "-m", "two")
	other, otherTree := gitT(t, f.repo, "rev-parse", "HEAD"), gitT(t, f.repo, "rev-parse", "HEAD^{tree}")
	gitT(t, f.repo, "replace", f.commit, other)
	db := f.openDB(t)
	defer func() { _ = db.Close() }()
	realTree := f.tree
	f.tree = otherTree // the record and its accepted fact name the replacement's tree
	f.store(t, db, "repo-a", f.accept(t, db, "repo-a", "ckpt-1"), "ckpt-1", 0)
	if err := f.runInProcess(t, f.commit, "--require", "--repo-id", "repo-a"); cascade.ExitCode(err) != 1 {
		t.Fatalf("record of the replacement tree satisfied --require for the real tree %s: %v", realTree, err)
	}
}

// TestCiAttestVerifyRefusesPairedDevice: a paired-device key never derives
// an eligible attestation.
func TestCiAttestVerifyRefusesPairedDevice(t *testing.T) {
	f := newAttestFixture(t)
	backend := nodes.NewFileRecordBackend(f.dataDir)
	all, err := backend.Load()
	if err != nil {
		t.Fatal(err)
	}
	rec := all[f.node.NodeID]
	rec.Tier = nodes.TierPairedDevice
	all[f.node.NodeID] = rec
	if err := backend.Save(all); err != nil {
		t.Fatal(err)
	}
	db := f.openDB(t)
	defer func() { _ = db.Close() }()
	f.store(t, db, "repo-a", f.accept(t, db, "repo-a", "ckpt-1"), "ckpt-1", 0)
	if err := f.runInProcess(t, f.commit, "--require", "--repo-id", "repo-a"); cascade.ExitCode(err) != 1 {
		t.Fatalf("paired-device record: %v, want exit 1", err)
	}
}

// TestCiAttestVerifyRefusesBadRecords: a stored row whose columns agree with
// its bytes but whose signature is wrong, or whose signed window has passed,
// fails verification (not merely "not eligible"), with or without --require.
func TestCiAttestVerifyRefusesBadRecords(t *testing.T) {
	for name, bad := range map[string]func(*attestFixture, *sql.DB, string){
		"expired": func(f *attestFixture, db *sql.DB, nonce string) {
			f.storeWith(t, db, "repo-a", nonce, "ckpt-1", func(r *attest.AttestationRecord) {
				r.StartedAt, r.EndedAt, r.ExpiresAt = f.start.Add(-3*time.Hour), f.start.Add(-2*time.Hour), f.start.Add(-time.Hour)
			})
		},
		"bad signature": func(f *attestFixture, db *sql.DB, nonce string) {
			row := f.store(t, db, "repo-a", nonce, "ckpt-1", 0)
			flipped := map[byte]string{'A': "B"}[row.Signature[0]]
			if flipped == "" {
				flipped = "A"
			}
			if _, err := db.Exec(`UPDATE ci_attestation SET signature = ? WHERE id = ?`, flipped+row.Signature[1:], row.ID); err != nil {
				t.Fatal(err)
			}
		},
	} {
		f := newAttestFixture(t)
		db := f.openDB(t)
		bad(f, db, f.accept(t, db, "repo-a", "ckpt-1"))
		_ = db.Close()
		for _, args := range [][]string{{f.commit, "--repo-id", "repo-a"}, {f.commit, "--repo-id", "repo-a", "--require"}} {
			err := f.runInProcess(t, args...)
			if cascade.ExitCode(err) != 1 || !strings.Contains(fmt.Sprint(err), "does not verify") {
				t.Errorf("%s %v: %v, want exit 1 from a verification failure", name, args[1:], err)
			}
		}
	}
}

// TestOpenAttestDBIsReadOnly: the verb's database handle refuses writes,
// creates no write-ahead files, and refuses a half-present sidecar pair.
func TestOpenAttestDBIsReadOnly(t *testing.T) {
	f := newAttestFixture(t)
	db := f.openDB(t)
	_ = db.Close()
	path := filepath.Join(f.dataDir, "cascade.db")
	ro, err := openAttestDB(context.Background(), path)
	if err != nil || ro == nil {
		t.Fatalf("openAttestDB = %v, %v", ro, err)
	}
	if _, err := ro.Exec(`DELETE FROM attestation_nonce`); err == nil {
		t.Fatal("a write through the verify handle succeeded")
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}
	for _, side := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + side); err == nil {
			t.Fatalf("opening read-only created %s", side)
		}
	}
	if err := os.WriteFile(path+"-shm", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openAttestDB(context.Background(), path); cascade.ExitCode(err) != 5 {
		t.Fatalf("half-present sidecar: %v, want exit 5 (try again)", err)
	}
}

// TestResolveAttestSubjectRefusesOptionShapedRevisions: an empty, option-
// shaped or whitespace/NUL revision is refused before git runs at all.
func TestResolveAttestSubjectRefusesOptionShapedRevisions(t *testing.T) {
	calls := 0
	deps := ciAttestVerifyDeps{Git: func(context.Context, ...string) (string, error) { calls++; return "x", nil }}
	for _, rev := range []string{"", "-x", "--output=/dev/null", "a b", "a\tb", "a\nb", "a\x00b"} {
		_, err := resolveAttestSubject(context.Background(), deps, rev, "repo-a")
		if cascade.ExitCode(err) != 2 || !strings.Contains(fmt.Sprint(err), "not an option") {
			t.Errorf("resolveAttestSubject(%q) = %v, want the option guard's refusal", rev, err)
		}
	}
	if calls != 0 {
		t.Fatalf("git ran %d times for refused revisions", calls)
	}
	if _, err := resolveAttestSubject(context.Background(), deps, "HEAD", "repo-a"); err != nil || calls == 0 {
		t.Fatalf("a plain revision: %v after %d git calls, want it resolved through git", err, calls)
	}
}
