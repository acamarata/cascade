package jobs

// Purpose: R-21.194's named rollback-evidence artifact class -- the data
//
//	a release/CD gate (release_gate.go) requires alongside human approval
//	before a Critical-class release job may proceed (Epic AH DECIDED
//	R-16.13: "Release/CD gate = Critical class: explicit human approval +
//	rollback evidence"). RollbackEvidence is a NAMED struct, never a
//	free-form string, so "rollback evidence" always names a real,
//	structured artifact rather than a convention a caller might skip.
//
// Inputs: a RollbackEvidence value and the ReleaseCandidate it is being
//
//	verified against (release_gate.go).
//
// Outputs: VerifyRollbackEvidence's nil (verified) or typed
//
//	ErrMissingRollbackEvidence; rollbackEvidenceArtifactRef's
//	content-addressed reference string for a value that already passed
//	verification.
//
// Constraints: all five fields must be populated, VerificationResult must
//
//	be OutcomePass (evidence.go's existing three-member Outcome, reused
//	rather than inventing a new vocabulary), and ProducedByJobID must name
//	a job that completed strictly BEFORE the release gate node was
//	reached -- rb.ProducedAt.Before(cand.GateReachedAt) -- so a rollback
//	asserted after the fact is refused rather than accepted as evidence.
//	Any unmet condition fails closed with ErrMissingRollbackEvidence; no
//	permissive default and no empty ArtifactRef ever reaches a caller.
//
// SPORT: jobs/release-gate/ADD (P1-E34-W7-S70-T2).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrMissingRollbackEvidence is refused for any RollbackEvidence that
// fails VerifyRollbackEvidence: an unpopulated field, a non-Pass
// VerificationResult, or a ProducedByJobID naming a job that did not
// complete before the gate node was reached. KindInvalidInput, matching
// evidence.go's own validateEvidenceRecord posture for an incomplete or
// malformed evidence-shaped value.
var ErrMissingRollbackEvidence = cascade.New(cascade.KindInvalidInput, "jobs: missing or unverified rollback evidence")

// RollbackEvidence is R-21.194's named artifact class: the five fields a
// release/CD gate requires before releasing, never a free-form string.
type RollbackEvidence struct {
	// PreviousReleaseRef names the release this rollback would restore.
	PreviousReleaseRef string
	// RollbackCommand is the tested command that performs the rollback.
	RollbackCommand string
	// VerificationResult is the Outcome of actually exercising
	// RollbackCommand -- must be OutcomePass for the evidence to verify.
	VerificationResult Outcome
	// ProducedByJobID names the job whose run produced this evidence. It
	// must have COMPLETED before the release gate node was reached.
	ProducedByJobID string
	// ProducedAt is when ProducedByJobID's run produced this evidence.
	ProducedAt time.Time
}

// VerifyRollbackEvidence checks rb against cand: every field populated,
// VerificationResult == OutcomePass, and ProducedByJobID's run having
// completed (rb.ProducedAt) strictly before cand.GateReachedAt -- a
// rollback asserted after the gate node was already reached is not
// evidence of anything. Any unmet condition returns
// ErrMissingRollbackEvidence; nil means rb is real, complete, and timely.
func VerifyRollbackEvidence(rb RollbackEvidence, cand ReleaseCandidate) error {
	switch {
	case rb.PreviousReleaseRef == "":
		return cascade.Wrapf(cascade.KindInvalidInput, ErrMissingRollbackEvidence,
			"jobs: rollback evidence missing previous_release_ref")
	case rb.RollbackCommand == "":
		return cascade.Wrapf(cascade.KindInvalidInput, ErrMissingRollbackEvidence,
			"jobs: rollback evidence missing rollback_command")
	case rb.VerificationResult == "":
		return cascade.Wrapf(cascade.KindInvalidInput, ErrMissingRollbackEvidence,
			"jobs: rollback evidence missing verification_result")
	case rb.ProducedByJobID == "":
		return cascade.Wrapf(cascade.KindInvalidInput, ErrMissingRollbackEvidence,
			"jobs: rollback evidence missing produced_by_job_id")
	case rb.ProducedAt.IsZero():
		return cascade.Wrapf(cascade.KindInvalidInput, ErrMissingRollbackEvidence,
			"jobs: rollback evidence missing produced_at")
	}
	if rb.VerificationResult != OutcomePass {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrMissingRollbackEvidence,
			"jobs: rollback evidence verification result %q is not pass", string(rb.VerificationResult))
	}
	if cand.GateReachedAt.IsZero() {
		return cascade.New(cascade.KindInvalidInput, "jobs: release candidate missing gate_reached_at")
	}
	if !rb.ProducedAt.Before(cand.GateReachedAt) {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrMissingRollbackEvidence,
			"jobs: rollback evidence for job %q was not produced before the gate node was reached", rb.ProducedByJobID)
	}
	return nil
}

// rollbackEvidenceCanonical is the fixed wire shape rollbackEvidenceArtifactRef
// hashes over -- field order fixed by this struct's declaration, never a
// map, so the same rb always yields the same ref.
type rollbackEvidenceCanonical struct {
	PreviousReleaseRef string `json:"previous_release_ref"`
	RollbackCommand    string `json:"rollback_command"`
	VerificationResult string `json:"verification_result"`
	ProducedByJobID    string `json:"produced_by_job_id"`
	ProducedAtUnix     int64  `json:"produced_at"`
}

// rollbackEvidenceArtifactRef computes the content-addressed reference a
// VERIFIED RollbackEvidence value serializes to: the hex-SHA256 digest of
// its canonical JSON, prefixed so the ref self-describes its content
// type. Callers must run VerifyRollbackEvidence first -- this function
// does not re-check the fields, it only names the bytes.
func rollbackEvidenceArtifactRef(rb RollbackEvidence) string {
	raw, _ := json.Marshal(rollbackEvidenceCanonical{
		PreviousReleaseRef: rb.PreviousReleaseRef,
		RollbackCommand:    rb.RollbackCommand,
		VerificationResult: string(rb.VerificationResult),
		ProducedByJobID:    rb.ProducedByJobID,
		ProducedAtUnix:     rb.ProducedAt.UTC().Unix(),
	})
	sum := sha256.Sum256(raw)
	return "rollback-evidence:" + hex.EncodeToString(sum[:])
}
