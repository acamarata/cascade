package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/ci/attest"
)

var attestStart = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// sampleAttestationRecord is a valid record; signing is not needed to
// store a row (the controller verifies before Insert).
func sampleAttestationRecord(nodeID, nonce, tree string) attest.AttestationRecord {
	return attest.AttestationRecord{
		SchemaVersion: 1, AttestorClass: attest.ClassEnrolledNode, CheckpointID: "ckpt-1",
		CommitSHA: strings.Repeat("a1", 20), TreeHash: tree, EndedAt: attestStart.Add(10 * time.Minute),
		ExpiresAt: attestStart.Add(12 * time.Hour), JobSpecHash: strings.Repeat("3c", 32), KeyID: "k-" + nodeID,
		NodeID: nodeID, Nonce: nonce, PolicyVersion: "policy-v1", RepoID: "repo-a",
		ResultHashes: []string{"lint:" + strings.Repeat("1", 64)}, StartedAt: attestStart,
		TargetSelection: "affected:./internal/jobs", ToolchainFP: "go1.26",
	}
}

// attestationRowFor projects r into a ci_attestation row.
func attestationRowFor(t *testing.T, r attest.AttestationRecord) CiAttestationRow {
	t.Helper()
	canon, err := attest.CanonicalJSON(r)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	sa := attest.SignedAttestation{CanonicalJSON: canon, Signature: "sig-not-checked-by-the-store"}
	return CiAttestationRow{
		ID: attest.RowID(sa), NodeID: r.NodeID, RepoID: r.RepoID, CommitSHA: r.CommitSHA, TreeHash: r.TreeHash,
		JobSpecHash: r.JobSpecHash, ToolchainFP: r.ToolchainFP, StartedAt: r.StartedAt.Unix(), EndedAt: r.EndedAt.Unix(),
		ExitStatus: r.ExitStatus, Signature: sa.Signature, CanonicalJSON: string(canon), PolicyVersion: r.PolicyVersion,
		KeyID: r.KeyID, AttestorClass: string(attest.ClassEnrolledNode), CheckpointID: r.CheckpointID,
		TargetSelection: r.TargetSelection, Nonce: r.Nonce, ExpiresAt: r.ExpiresAt.Unix(),
		AcceptedBeforeRevocation: true, CreatedAt: attestStart.Add(time.Hour).Unix(),
	}
}

func TestCiAttestationStore(t *testing.T) {
	st, err := NewCiAttestationStore(newTestStore(t).db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tree := strings.Repeat("b2", 20)
	row := attestationRowFor(t, sampleAttestationRecord("node-1", "n1", tree))
	if err := st.Insert(ctx, row); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, ok, err := st.ByID(ctx, row.ID)
	if err != nil || !ok || got != row {
		t.Fatalf("ByID = %+v, %v, %v; want %+v", got, ok, err, row)
	}
	if got.TargetSelection != "affected:./internal/jobs" || got.ID != attest.RowID(attest.SignedAttestation{CanonicalJSON: []byte(got.CanonicalJSON)}) {
		t.Fatalf("stored row lost target_selection or its RowID: %+v", got)
	}
	byCommit, err := st.ByCommitSHA(ctx, row.CommitSHA)
	if err != nil || len(byCommit) != 1 || byCommit[0] != row {
		t.Fatalf("ByCommitSHA = %+v, %v", byCommit, err)
	}
	byTree, err := st.ByCommitAndTree(ctx, row.CommitSHA, row.TreeHash)
	if err != nil || len(byTree) != 1 || byTree[0] != row {
		t.Fatalf("ByCommitAndTree = %+v, %v", byTree, err)
	}
	if other, err := st.ByCommitAndTree(ctx, row.CommitSHA, strings.Repeat("d4", 20)); err != nil || len(other) != 0 {
		t.Fatalf("ByCommitAndTree(other tree) = %+v, %v; want none", other, err)
	}
	if _, ok, err := st.ByID(ctx, "missing"); ok || err != nil {
		t.Fatalf("ByID(missing) = %v, %v", ok, err)
	}
	assertAttestationDuplicates(t, st, row, tree)
}

// assertAttestationDuplicates: a duplicate id or slot is refused and
// changes nothing; another node's row for the same commit is stored.
func assertAttestationDuplicates(t *testing.T, st *CiAttestationStore, row CiAttestationRow, tree string) {
	t.Helper()
	ctx := context.Background()
	changed := row
	changed.CreatedAt++
	if err := st.Insert(ctx, changed); !sentinelOnChain(err, ErrAlreadyAttested) {
		t.Fatalf("Insert(same id) = %v, want ErrAlreadyAttested", err)
	}
	sameSlot := attestationRowFor(t, sampleAttestationRecord("node-1", "n2", tree))
	if err := st.Insert(ctx, sameSlot); !sentinelOnChain(err, ErrAlreadyAttested) {
		t.Fatalf("Insert(same node, commit, tree, checkpoint) = %v, want ErrAlreadyAttested", err)
	}
	if got, _, _ := st.ByID(ctx, row.ID); got != row {
		t.Fatalf("a refused duplicate changed the stored row: %+v", got)
	}
	second := attestationRowFor(t, sampleAttestationRecord("node-2", "n3", tree))
	if err := st.Insert(ctx, second); err != nil {
		t.Fatalf("Insert(second node) = %v", err)
	}
	if all, _ := st.ByCommitAndTree(ctx, row.CommitSHA, tree); len(all) != 2 {
		t.Fatalf("ByCommitAndTree after two nodes = %d rows, want 2", len(all))
	}
}

func TestCiAttestationStoreRefusesInconsistentRows(t *testing.T) {
	st, _ := NewCiAttestationStore(newTestStore(t).db)
	ctx := context.Background()
	row := attestationRowFor(t, sampleAttestationRecord("node-1", "n1", strings.Repeat("b2", 20)))
	cases := map[string]func(*CiAttestationRow){
		"id not the digest":     func(r *CiAttestationRow) { r.ID = strings.Repeat("0", 64) },
		"column disagrees":      func(r *CiAttestationRow) { r.ExitStatus = 1 },
		"repo column disagrees": func(r *CiAttestationRow) { r.RepoID = "repo-b" },
		"class not a class":     func(r *CiAttestationRow) { r.AttestorClass = "root" },
		"time column disagrees": func(r *CiAttestationRow) { r.ExpiresAt++ },
		"canonical not decoded": func(r *CiAttestationRow) {
			r.CanonicalJSON += " "
			r.ID = attest.RowID(attest.SignedAttestation{CanonicalJSON: []byte(r.CanonicalJSON)})
		},
	}
	for name, mutate := range cases {
		bad := row
		mutate(&bad)
		if err := st.Insert(ctx, bad); err == nil {
			t.Errorf("Insert(%s) = nil, want a refusal", name)
		}
	}
	if _, err := NewCiAttestationStore(nil); err == nil {
		t.Error("NewCiAttestationStore(nil) = nil error")
	}
}

// sentinelOnChain reports whether target appears by identity on err's chain.
func sentinelOnChain(err, target error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if err == target {
			return true
		}
	}
	return false
}
