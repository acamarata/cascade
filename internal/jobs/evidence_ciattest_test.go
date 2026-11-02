package jobs

import (
	"context"
	"testing"
)

func TestAttestationArtifactRefInChain(t *testing.T) {
	cp, store, req, row := attestedCompletion(t, RiskClassNormal, false)
	appendAttestedEvidence(t, cp, req, row)
	extra := passRecord(EvidenceTests, "after-attestation")
	if _, err := cp.deps.Ledger.Append(context.Background(), extra, baseAuth()); err != nil {
		t.Fatal(err)
	}
	if err := cp.deps.Ledger.Verify(context.Background(), req.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE jobs_evidence SET artifact_ref = 'ci_attestation:tampered' WHERE kind = 'ci_attestation'`); err != nil {
		t.Fatal(err)
	}
	if err := cp.deps.Ledger.Verify(context.Background(), req.Job.ID); err == nil {
		t.Fatal("artifact mutation did not break chain")
	}
}

func testUnattestedClassGates(t *testing.T) {
	for _, risk := range []RiskClass{RiskClassNormal, RiskClassHigh, RiskClassCritical} {
		t.Run(string(risk), func(t *testing.T) {
			cp, store, req, row := attestedCompletion(t, risk, false)
			if _, err := store.db.Exec(`UPDATE ci_attestation SET attestor_class = 'unattested', signature = '' WHERE id = ?`, row.ID); err != nil {
				t.Fatal(err)
			}
			appendAttestedEvidence(t, cp, req, row)
			err := cp.Transition(context.Background(), req)
			if risk == RiskClassNormal {
				if err != nil || storedJobState(t, store, req.Job.ID) != JobStateAccepted {
					t.Fatalf("Normal unattested = %v", err)
				}
			} else if err == nil || storedJobState(t, store, req.Job.ID) != JobStateReviewing {
				t.Fatalf("%s unattested = %v", risk, err)
			}
		})
	}
}
