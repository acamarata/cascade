package jobs

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

func attestedCompletion(t *testing.T, risk RiskClass, failed bool) (*CompletionPolicy, *Store, TransitionRequest, CiAttestationRow) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cp, store, ledger, _, job := newCompletionFixture(t, risk)
	setJobState(t, store, &job, JobStateReviewing)
	passAll(t, ledger, []EvidenceKind{EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview, EvidenceAdversarial, EvidenceQA})
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	trust := attest.NewControllerTrustStore(nodes.Identity{NodeID: "controller", PubKey: pub}, nil)
	cp.deps.AttestationTrust = trust
	nonces, _ := NewAttestationNonceStore(store.db, cp.deps.Clock)
	nonce, expires, err := nonces.Issue(context.Background(), job.ID, "cp-1", "registry-repo", JobSpecHash(job, nil), "policy-v1", []string{"lint"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r := sampleAttestationRecord("controller", nonce, strings.Repeat("b", 40))
	r.CheckpointID, r.KeyID, r.RepoID, r.JobSpecHash = "cp-1", nodes.KeyIDFor(pub), "registry-repo", JobSpecHash(job, nil)
	r.StartedAt, r.EndedAt, r.ExpiresAt = cp.deps.Clock.Now(), cp.deps.Clock.Now(), expires
	if failed {
		r.ExitStatus = 1
	}
	sa, err := attest.Attest(r, func(p []byte) ([]byte, error) { return ed25519.Sign(key, p), nil })
	if err != nil {
		t.Fatal(err)
	}
	row := attestationRowFor(t, r)
	row.Signature = sa.Signature
	rows, _ := NewCiAttestationStore(store.db)
	if err := rows.Insert(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	req := TransitionRequest{Job: &job, Target: JobStateAccepted, Caller: PolicyEngineIdentity{EngineID: testEngineID}, PlannedRiskClass: risk, CheckpointID: r.CheckpointID, TreeHash: r.TreeHash}
	cp.deps.Acceptance = acceptanceVerifierFunc(func(context.Context, string) (AcceptanceVerdict, error) {
		return AcceptanceVerdict{Passed: true, CheckpointID: req.CheckpointID, TreeHash: req.TreeHash, AttestationNonce: nonce}, nil
	})
	return cp, store, req, row
}

func appendAttestedEvidence(t *testing.T, cp *CompletionPolicy, _ TransitionRequest, row CiAttestationRow) {
	t.Helper()
	rec := passRecord(EvidenceCIAttestation, "attestation-"+row.ID)
	rec.ProducerCapability, rec.AttestorIdentity = ProducerAttestor, "node:"+row.NodeID
	rec.CheckpointID, rec.TreeHash, rec.ArtifactRef = row.CheckpointID, row.TreeHash, "ci_attestation:"+row.ID
	if _, err := cp.deps.Ledger.Append(context.Background(), rec, baseAuth()); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptanceTransitionConsumesNonceOnce(t *testing.T) {
	cp, store, req, row := attestedCompletion(t, RiskClassNormal, false)
	ctx := context.Background()
	if err := cp.Transition(ctx, req); err != nil {
		t.Fatal(err)
	}
	nonces, _ := NewAttestationNonceStore(store.db, cp.deps.Clock)
	if _, ok, err := nonces.AcceptedFor(ctx, row.Nonce, row.CommitSHA, row.TreeHash, row.CheckpointID); err != nil || !ok {
		t.Fatalf("not consumed: %v %v", ok, err)
	}
	setJobState(t, store, req.Job, JobStateReviewing)
	if err := cp.Transition(ctx, req); !sentinelOnChain(err, attest.ErrNonceReused) {
		t.Fatalf("reuse = %v", err)
	}
	if storedJobState(t, store, req.Job.ID) != JobStateReviewing {
		t.Fatal("reuse changed state")
	}
	t.Run("expired rollback", func(t *testing.T) {
		cp, store, req, row := attestedCompletion(t, RiskClassNormal, false)
		if _, err := store.db.Exec(`UPDATE attestation_nonce SET expires_at = 0 WHERE nonce = ?`, row.Nonce); err != nil {
			t.Fatal(err)
		}
		if err := cp.Transition(ctx, req); !sentinelOnChain(err, attest.ErrExpiredAttestation) {
			t.Fatalf("expired = %v", err)
		}
		var consumed sql.NullInt64
		if err := store.db.QueryRow(`SELECT consumed_at FROM attestation_nonce WHERE nonce = ?`, row.Nonce).Scan(&consumed); err != nil || consumed.Valid {
			t.Fatalf("rollback consumed nonce: %v", err)
		}
		if storedJobState(t, store, req.Job.ID) != JobStateReviewing {
			t.Fatal("expired changed state")
		}
	})
}

func TestHighCriticalRequireEligibleAttestation(t *testing.T) {
	for _, risk := range []RiskClass{RiskClassHigh, RiskClassCritical} {
		t.Run(string(risk), func(t *testing.T) {
			cp, _, req, row := attestedCompletion(t, risk, false)
			missing, err := cp.missingEvidence(context.Background(), req, highGates)
			if err != nil || !containsEvidence(missing, EvidenceCIAttestation) {
				t.Fatalf("unattested %v %v", missing, err)
			}
			if err := cp.Transition(context.Background(), req); err == nil {
				t.Fatal("missing attestation reached accepted")
			}
			appendAttestedEvidence(t, cp, req, row)
			if !cp.eligibleAttestation(context.Background(), req, mustEvidence(t, cp)) {
				t.Fatal("valid proof refused")
			}
			cp.deps.AttestationTrust = attest.NewControllerTrustStore(nodes.Identity{}, nil)
			if cp.eligibleAttestation(context.Background(), req, mustEvidence(t, cp)) {
				t.Fatal("revoked proof accepted")
			}
		})
	}
	t.Run("unattested class", testUnattestedClassGates)
}

func containsEvidence(kinds []EvidenceKind, kind EvidenceKind) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}

func mustEvidence(t *testing.T, cp *CompletionPolicy) EvidenceRecord {
	t.Helper()
	rec, err := cp.deps.Ledger.Query(context.Background(), "job-1", EvidenceCIAttestation)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestFailedRerunAttestationNotEligible(t *testing.T) {
	cp, store, req, row := attestedCompletion(t, RiskClassHigh, true)
	appendAttestedEvidence(t, cp, req, row)
	if err := cp.Transition(context.Background(), req); err == nil {
		t.Fatal("failed signed run accepted")
	}
	if storedJobState(t, store, req.Job.ID) != JobStateReviewing {
		t.Fatal("failed run changed state")
	}
}

func TestHighGateRefusesWithoutEligibilityTrust(t *testing.T) {
	cp, _, req, row := attestedCompletion(t, RiskClassHigh, false)
	appendAttestedEvidence(t, cp, req, row)
	trust := cp.deps.AttestationTrust
	cp.deps.AttestationTrust = nil
	if err := cp.Transition(context.Background(), req); err == nil {
		t.Fatal("nil trust accepted")
	}
	cp.deps.AttestationTrust = trust
	if !cp.eligibleAttestation(context.Background(), req, mustEvidence(t, cp)) {
		t.Fatal("positive control failed with restored trust")
	}
	if _, err := cp.deps.Store.db.Exec(`UPDATE ci_attestation SET signature = 'forged' WHERE id = ?`, row.ID); err != nil {
		t.Fatal(err)
	}
	if cp.eligibleAttestation(context.Background(), req, mustEvidence(t, cp)) {
		t.Fatal("forged signature accepted")
	}
}

func TestNonceConsumeConcurrent(t *testing.T) {
	cp, store, req, row := attestedCompletion(t, RiskClassNormal, false)
	nonces, _ := NewAttestationNonceStore(store.db, cp.deps.Clock)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- consume(t, store.db, nonces, row.Nonce, req.Job.ID, row.CommitSHA, row.TreeHash, cp.deps.Clock.Now())
		}()
	}
	wg.Wait()
	close(errs)
	success, reused := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if sentinelOnChain(err, attest.ErrNonceReused) {
			reused++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || reused != 15 {
		t.Fatalf("success=%d reused=%d", success, reused)
	}
	if _, err := store.db.Exec(`UPDATE attestation_nonce SET consumed_at = NULL`); err != nil {
		t.Fatal(err)
	}
	assertConcurrentTransitions(t, cp, req)
}

func assertConcurrentTransitions(t *testing.T, cp *CompletionPolicy, req TransitionRequest) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := req
			job := *req.Job
			local.Job = &job
			errs <- cp.Transition(context.Background(), local)
		}()
	}
	wg.Wait()
	close(errs)
	success, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if cascade.HasKind(err, cascade.KindConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 15 {
		t.Fatalf("accepted=%d conflicts=%d", success, conflicts)
	}
}
