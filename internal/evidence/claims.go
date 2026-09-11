// Package evidence is the AQ claim/evidence record layer (R-21.41, as
// amended by R-21.94/R-21.86): the Claim and Evidence rows, their
// persistence in the jobs storage domain, the evidence index addressed by
// evidence://<run_id>, and the bounded conductor.expand packet that
// resolves a claim back to retrievable bytes. A summary is an index over
// evidence, never a lossy replacement (R-21.41) -- this package is the
// substrate every other AQ ticket writes through.
//
// Purpose: Claim is the typed record of one extracted statement: what was
// said, how confident the extractor was, which run/lane produced it, and
// whether later verification corroborated or contradicted it.
// Inputs: none (types only, plus crypto/rand via pkg/cascade.NewID).
// Outputs: a Claim value, or a typed invalid-input error from Validate.
// Constraints: ClaimType and VerificationResult are closed enums with no
// permissive zero value (06 SS5.15/SS5.16); Confidence is bounded to
// [0,1]; DataClass (R-21.94) is immutable once a claim is stored --
// enforced in claim_store.go, not here.
//
// SPORT: evidence/claim-record (ADD).
package evidence

import (
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ClaimIDPrefix names every minted claim id.
const ClaimIDPrefix = "CLM-"

// ClaimType is the closed three-value vocabulary a Claim's Statement is
// classified under (R-21.41). The zero value is deliberately not a
// member.
type ClaimType string

const (
	// ClaimObservedFact is a statement directly grounded in evidence.
	ClaimObservedFact ClaimType = "observed_fact"
	// ClaimInference is a statement derived from observed facts.
	ClaimInference ClaimType = "inference"
	// ClaimHypothesis is an unconfirmed candidate statement.
	ClaimHypothesis ClaimType = "hypothesis"
)

// Valid reports whether t is one of the three closed values.
func (t ClaimType) Valid() bool {
	switch t {
	case ClaimObservedFact, ClaimInference, ClaimHypothesis:
		return true
	}
	return false
}

// DecodeClaimType parses a stored value into a ClaimType. An unset,
// unknown, or unparseable value is a typed invalid-input error -- never a
// silent default (06 SS5.15/SS5.16 fail-closed discipline).
func DecodeClaimType(raw string) (ClaimType, error) {
	t := ClaimType(raw)
	if !t.Valid() {
		return "", cascade.Newf(cascade.KindInvalidInput,
			"evidence: unknown claim type %q: must be one of observed_fact, inference, hypothesis", raw)
	}
	return t, nil
}

// VerificationResult is the closed three-value outcome of one verification
// pass over a Claim (R-21.41). The zero value is deliberately not a
// member.
type VerificationResult string

const (
	// VerificationCorroborated reports that a verification run supported
	// the claim.
	VerificationCorroborated VerificationResult = "corroborated"
	// VerificationContradicted reports that a verification run refuted
	// the claim.
	VerificationContradicted VerificationResult = "contradicted"
	// VerificationUnverified reports that a verification run reached no
	// conclusion.
	VerificationUnverified VerificationResult = "unverified"
)

// Valid reports whether r is one of the three closed values.
func (r VerificationResult) Valid() bool {
	switch r {
	case VerificationCorroborated, VerificationContradicted, VerificationUnverified:
		return true
	}
	return false
}

// DecodeVerificationResult parses a stored value into a
// VerificationResult. An unset, unknown, or unparseable value is a typed
// invalid-input error.
func DecodeVerificationResult(raw string) (VerificationResult, error) {
	r := VerificationResult(raw)
	if !r.Valid() {
		return "", cascade.Newf(cascade.KindInvalidInput,
			"evidence: unknown verification result %q: must be one of corroborated, contradicted, unverified", raw)
	}
	return r, nil
}

// ProducedBy names the run and lane that produced a Claim (R-21.41).
type ProducedBy struct {
	RunID  string `json:"run_id"`
	LaneID string `json:"lane_id"`
}

// Verification is one verification pass's outcome against a Claim
// (R-21.41).
type Verification struct {
	RunID  string             `json:"run_id"`
	Result VerificationResult `json:"result"`
}

// Claim is the R-21.41 claim record verbatim, plus the ONE field R-21.94
// adds: an immutable DataClass carrying the sensitivity tier of the
// material the claim was extracted from. InvalidatedAt is the plan's
// later-invalidation flag: nil while the claim stands, set to the
// injected clock's timestamp once the claim is retired.
type Claim struct {
	ID             string         `json:"id"`
	Statement      string         `json:"statement"`
	Type           ClaimType      `json:"type"`
	Confidence     float64        `json:"confidence"`
	ProducedBy     ProducedBy     `json:"produced_by"`
	Verification   []Verification `json:"verification"`
	Contradictions []string       `json:"contradictions"`
	InvalidatedAt  *time.Time     `json:"invalidated_at,omitempty"`
	DataClass      DataClass      `json:"data_class"`
}

// NewClaimID mints a fresh claim identifier: ClaimIDPrefix plus the
// 26-character Crockford-base32 id from pkg/cascade.NewID (R-16.47: no
// google/uuid anywhere in this package).
func NewClaimID() (string, error) {
	id, err := cascade.NewID()
	if err != nil {
		return "", err
	}
	return ClaimIDPrefix + id.String(), nil
}

// Validate rejects an out-of-range Confidence, an unknown ClaimType, an
// unknown DataClass, or a Verification entry carrying an unknown Result --
// every rejection is a typed invalid-input error, never a silent
// acceptance of an unparseable value.
func (c Claim) Validate() error {
	if c.Statement == "" {
		return cascade.New(cascade.KindInvalidInput, "evidence: claim statement is required")
	}
	if !c.Type.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "evidence: unknown claim type %q", string(c.Type))
	}
	if c.Confidence < 0 || c.Confidence > 1 {
		return cascade.Newf(cascade.KindInvalidInput, "evidence: confidence %v is outside [0,1]", c.Confidence)
	}
	if !c.DataClass.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "evidence: unknown data_class %q", string(c.DataClass))
	}
	if c.ProducedBy.RunID == "" {
		return cascade.New(cascade.KindInvalidInput, "evidence: claim produced_by.run_id is required")
	}
	for _, v := range c.Verification {
		if _, err := DecodeVerificationResult(string(v.Result)); err != nil {
			return err
		}
	}
	return nil
}
