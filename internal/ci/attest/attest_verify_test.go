package attest_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/testkit"
)

var updateGolden = flag.Bool("update-attestation-golden", false, "regenerate testdata/attestation/signed_attestation.json")

const goldenPath = "testdata/attestation/signed_attestation.json"

// goldenFile is the committed signed attestation plus its public key.
type goldenFile struct {
	CanonicalJSON string `json:"canonical_json"`
	Signature     string `json:"signature"`
	NodeID        string `json:"node_id"`
	PublicKeyB64  string `json:"public_key_b64"`
}

func verifyAt() time.Time { return attest.TestStart.Add(time.Hour) }

func TestAttestUnknownNode(t *testing.T) {
	n := attest.NewTestNode(t)
	other := attest.NewTestNode(t)
	r := other.SampleRecord()
	sa := other.MustAttest(t, r)
	err := attest.VerifySignature(context.Background(), sa, r.CommitSHA, r.TreeHash, n.Trust(), verifyAt())
	if !attest.IsSentinel(err, attest.ErrUnknownNode) {
		t.Fatalf("VerifySignature(node not in the store) = %v, want ErrUnknownNode", err)
	}
}

func TestAttestExpired(t *testing.T) {
	n := attest.NewTestNode(t)
	r := n.SampleRecord()
	sa := n.MustAttest(t, r)
	for name, now := range map[string]time.Time{"at expiry": r.ExpiresAt, "after expiry": r.ExpiresAt.Add(time.Second)} {
		err := attest.VerifySignature(context.Background(), sa, r.CommitSHA, r.TreeHash, n.Trust(), now)
		if !attest.IsSentinel(err, attest.ErrExpiredAttestation) {
			t.Errorf("%s: VerifySignature = %v, want ErrExpiredAttestation", name, err)
		}
	}
	future := n.SampleRecord()
	future.StartedAt, future.EndedAt, future.ExpiresAt = verifyAt().Add(10*time.Minute), verifyAt().Add(15*time.Minute), verifyAt().Add(20*time.Minute)
	sa = n.MustAttest(t, future)
	if err := attest.VerifySignature(context.Background(), sa, future.CommitSHA, future.TreeHash, n.Trust(), verifyAt()); !attest.IsSentinel(err, attest.ErrExpiredAttestation) {
		t.Fatalf("VerifySignature(started in the future) = %v, want ErrExpiredAttestation", err)
	}
	for name, mutate := range map[string]func(*attest.AttestationRecord){
		"ended before started": func(r *attest.AttestationRecord) { r.EndedAt = r.StartedAt.Add(-time.Second) },
		"expires before ended": func(r *attest.AttestationRecord) { r.EndedAt = r.ExpiresAt.Add(time.Second) },
	} {
		r := n.SampleRecord()
		mutate(&r)
		err := attest.VerifySignature(context.Background(), n.MustAttest(t, r), r.CommitSHA, r.TreeHash, n.Trust(), verifyAt())
		if !attest.IsSentinel(err, attest.ErrExpiredAttestation) {
			t.Errorf("%s: VerifySignature = %v, want ErrExpiredAttestation", name, err)
		}
	}
	long := n.SampleRecord()
	long.ExpiresAt = long.StartedAt.Add(24*time.Hour + time.Second)
	sa = n.MustAttest(t, long)
	err := attest.VerifySignature(context.Background(), sa, long.CommitSHA, long.TreeHash, n.Trust(), verifyAt())
	if !attest.IsSentinel(err, attest.ErrExpiredAttestation) {
		t.Fatalf("VerifySignature(lifetime over 24 h) = %v, want ErrExpiredAttestation", err)
	}
}

func TestCommitAndTreeBinding(t *testing.T) {
	n := attest.NewTestNode(t)
	r := n.SampleRecord()
	sa := n.MustAttest(t, r)
	otherCommit, otherTree := strings.Repeat("c3", 20), strings.Repeat("d4", 20)
	cases := []struct {
		name, commit, tree string
		ok                 bool
	}{
		{"both match", r.CommitSHA, r.TreeHash, true},
		{"commit differs", otherCommit, r.TreeHash, false},
		{"tree differs", r.CommitSHA, otherTree, false},
		{"both differ", otherCommit, otherTree, false},
		{"empty commit", "", r.TreeHash, false},
		{"empty tree", r.CommitSHA, "", false},
	}
	for _, c := range cases {
		err := attest.VerifySignature(context.Background(), sa, c.commit, c.tree, n.Trust(), verifyAt())
		if c.ok != (err == nil) {
			t.Errorf("%s: VerifySignature = %v, want ok=%v", c.name, err, c.ok)
		}
		if !c.ok && !attest.IsSentinel(err, attest.ErrInvalidRecord) {
			t.Errorf("%s: VerifySignature = %v, want ErrInvalidRecord", c.name, err)
		}
	}
}

func TestVerifyIgnoresCallerRecord(t *testing.T) {
	n := attest.NewTestNode(t)
	r := n.SampleRecord()
	sa := n.MustAttest(t, r)
	forged := sa
	forged.Record.CommitSHA = strings.Repeat("c3", 20)
	forged.Record.TreeHash = strings.Repeat("d4", 20)
	forged.Record.ExitStatus = 0
	forged.Record.NodeID = "someone-else"
	if err := attest.VerifySignature(context.Background(), forged, r.CommitSHA, r.TreeHash, n.Trust(), verifyAt()); err != nil {
		t.Fatalf("VerifySignature against the signed content = %v, want nil", err)
	}
	err := attest.VerifySignature(context.Background(), forged, forged.Record.CommitSHA, forged.Record.TreeHash, n.Trust(), verifyAt())
	if !attest.IsSentinel(err, attest.ErrInvalidRecord) {
		t.Fatalf("VerifySignature(the forged Record's commit and tree) = %v, want ErrInvalidRecord", err)
	}
	if forged.Record.CommitSHA == r.CommitSHA {
		t.Fatal("fixture broken: a consumer reading sa.Record must see the forgery")
	}
}

// openJobsDB opens a fresh sqlite file under t.TempDir() with the jobs
// schema applied.
func openJobsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "cascade.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := jobs.ApplyJobsSchema(context.Background(), db, migrate.SQLiteEmitter{}, testkit.NewFrozenClock(attest.TestStart), "", ""); err != nil {
		t.Fatalf("ApplyJobsSchema: %v", err)
	}
	return db
}

// nonceRows dumps every attestation_nonce row as text.
func nonceRows(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT nonce, job_id, checkpoint_id, repo_id, job_spec_hash, policy_version,
		plan_kinds, issued_at, expires_at, consumed_at, accepted_commit, accepted_tree FROM attestation_nonce ORDER BY nonce`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out strings.Builder
	for rows.Next() {
		var v [12]any
		ptrs := make([]any, len(v))
		for i := range v {
			ptrs[i] = &v[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(&out, v)
	}
	return out.String()
}

func TestAttestVerifyDoesNotConsume(t *testing.T) {
	n := attest.NewTestNode(t)
	db := openJobsDB(t)
	nonces, err := jobs.NewAttestationNonceStore(db, testkit.NewFrozenClock(attest.TestStart))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	nonce, _, err := nonces.Issue(ctx, "job-1", "ckpt-1", "repo-a", strings.Repeat("3c", 32), "policy-v1", []string{"lint", "unit"}, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	r := n.SampleRecord()
	r.Nonce = nonce
	sa := n.MustAttest(t, r)
	before := nonceRows(t, db)
	if err := attest.VerifySignature(ctx, sa, r.CommitSHA, r.TreeHash, n.Trust(), verifyAt()); err != nil {
		t.Fatalf("VerifySignature: %v", err)
	}
	if got := attest.DeriveClass(sa, n.Trust(), nil); got != attest.ClassEnrolledNode {
		t.Fatalf("DeriveClass = %q", got)
	}
	if after := nonceRows(t, db); after != before || !strings.Contains(after, nonce) {
		t.Fatalf("verification changed attestation_nonce:\nbefore %s\nafter  %s", before, after)
	}
}

func TestAttestGolden(t *testing.T) {
	if *updateGolden {
		writeGolden(t)
	}
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	var g goldenFile
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	pub, err := nodes.ValidateIdentity(g.NodeID, g.PublicKeyB64)
	if err != nil {
		t.Fatal(err)
	}
	trust := attest.NewControllerTrustStore(pub, nil)
	sa := attest.SignedAttestation{CanonicalJSON: []byte(g.CanonicalJSON), Signature: g.Signature}
	rec, err := attest.DecodeCanonical(sa.CanonicalJSON)
	if err != nil {
		t.Fatalf("golden does not decode: %v", err)
	}
	if again, _ := attest.CanonicalJSON(rec); !bytes.Equal(again, sa.CanonicalJSON) {
		t.Fatalf("golden is not byte-for-byte canonical:\n%s\n%s", again, sa.CanonicalJSON)
	}
	if err := attest.VerifySignature(context.Background(), sa, rec.CommitSHA, rec.TreeHash, trust, verifyAt()); err != nil {
		t.Fatalf("golden does not verify: %v", err)
	}
	for i := range sa.CanonicalJSON {
		mutated := sa
		mutated.CanonicalJSON = bytes.Clone(sa.CanonicalJSON)
		mutated.CanonicalJSON[i] ^= 0x01
		if attest.VerifySignature(context.Background(), mutated, rec.CommitSHA, rec.TreeHash, trust, verifyAt()) == nil {
			t.Fatalf("golden verifies after flipping a bit of byte %d", i)
		}
	}
}

// writeGolden regenerates the golden through the real Attest path with a
// fresh node identity in a temp-HOME file custody.
func writeGolden(t *testing.T) {
	n := attest.NewTestNode(t)
	sa := n.MustAttest(t, n.SampleRecord())
	out, err := json.MarshalIndent(goldenFile{
		CanonicalJSON: string(sa.CanonicalJSON), Signature: sa.Signature,
		NodeID: n.ID.NodeID, PublicKeyB64: base64.StdEncoding.EncodeToString(n.ID.PubKey),
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goldenPath, append(out, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
