package jobs

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestEvidenceCIAttestationResolvesRow(t *testing.T) {
	cp, store, req, row := attestedCompletion(t, RiskClassNormal, false)
	rec := passRecord(EvidenceCIAttestation, "unknown-attestation")
	rec.AttestorIdentity, rec.ArtifactRef = "node:"+row.NodeID, "ci_attestation:unknown"
	rec.CheckpointID, rec.TreeHash = row.CheckpointID, row.TreeHash
	for _, artifact := range []string{"ci_attestation:unknown", "", "node:" + row.NodeID} {
		rec.ArtifactRef = artifact
		if _, err := cp.deps.Ledger.Append(context.Background(), rec, baseAuth()); !cascade.HasKind(err, cascade.KindPermissionDenied) {
			t.Fatalf("artifact %q = %v", artifact, err)
		}
	}
	rec.ArtifactRef = "ci_attestation:" + row.ID
	rec.AttestorIdentity = "node:foreign"
	if _, err := cp.deps.Ledger.Append(context.Background(), rec, baseAuth()); err == nil {
		t.Fatal("foreign node accepted")
	}
	if _, err := store.db.Exec(`UPDATE attestation_nonce SET job_id = 'foreign' WHERE nonce = ?`, row.Nonce); err != nil {
		t.Fatal(err)
	}
	rec.AttestorIdentity = "node:" + row.NodeID
	if _, err := cp.deps.Ledger.Append(context.Background(), rec, baseAuth()); err == nil {
		t.Fatal("foreign job accepted")
	}
	if _, err := store.db.Exec(`UPDATE attestation_nonce SET job_id = ? WHERE nonce = ?`, req.Job.ID, row.Nonce); err != nil {
		t.Fatal(err)
	}
	testEvidenceRejectsChangedFact(t, cp, row, rec)
	appendAttestedEvidence(t, cp, req, row)
}

func testEvidenceRejectsChangedFact(t *testing.T, cp *CompletionPolicy, row CiAttestationRow, rec EvidenceRecord) {
	t.Helper()
	for _, field := range []struct{ column, original string }{
		{"checkpoint_id", row.CheckpointID}, {"repo_id", row.RepoID},
		{"job_spec_hash", row.JobSpecHash}, {"policy_version", row.PolicyVersion},
	} {
		var before int
		if err := cp.deps.Store.db.QueryRow(`SELECT count(*) FROM jobs_evidence`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		query := `UPDATE attestation_nonce SET ` + field.column + ` = ? WHERE nonce = ?`
		if _, err := cp.deps.Store.db.Exec(query, "foreign-fact", row.Nonce); err != nil {
			t.Fatal(err)
		}
		_, appendErr := cp.deps.Ledger.Append(context.Background(), rec, baseAuth())
		if _, err := cp.deps.Store.db.Exec(query, field.original, row.Nonce); err != nil {
			t.Fatal(err)
		}
		if !cascade.HasKind(appendErr, cascade.KindPermissionDenied) {
			t.Fatalf("changed %s = %v", field.column, appendErr)
		}
		var after int
		if err := cp.deps.Store.db.QueryRow(`SELECT count(*) FROM jobs_evidence`).Scan(&after); err != nil || after != before {
			t.Fatalf("changed %s wrote evidence: before %d after %d: %v", field.column, before, after, err)
		}
	}
}

func TestPrefixOnlyAttestationRowSatisfiesNoGate(t *testing.T) {
	cp, _, req, row := attestedCompletion(t, RiskClassHigh, false)
	appendAttestedEvidence(t, cp, req, row)
	rec := mustEvidence(t, cp)
	rec.ArtifactRef = ""
	if cp.eligibleAttestation(context.Background(), req, rec) {
		t.Fatal("prefix-only row eligible")
	}
	if _, err := cp.deps.Ledger.Append(context.Background(), rec, baseAuth()); err == nil {
		t.Fatal("prefix-only append permitted")
	}
}
