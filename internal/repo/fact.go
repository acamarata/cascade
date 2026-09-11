package repo

// Purpose: the InferredFact record shape (R-16.17/R-21.180): a proposal
//   an extract-class model.execute call produced, holding EXACTLY the
//   decided field set {fact, source, confidence, version, accepted_by,
//   rollback}, its lifecycle states, and the R-21.180 CLOSED subject
//   enum. This is the type every other file in this ticket (ledger.go,
//   extract.go, policy.go) reads and writes.
// Inputs: none at this layer -- pure types plus their Validate methods.
// Outputs: InferredFact, FactState, FactSubject and their typed A-T7
//   refusals for anything outside the closed sets.
// Constraints: FactSubject has no `other` member and no permissive zero
//   value (R-21.180): a proposal naming any field outside the seven
//   closed subjects is refused at parse time, never coerced or dropped
//   silently.
// SPORT: repo/inferred-fact-ledger/ADD (P1-E33-W7-S67-T2).

import (
	"github.com/acamarata/cascade/pkg/cascade"
)

// FactSubject is the R-21.180 CLOSED enum of fact kinds the ledger will
// ever record. There is no `other` member: a proposal naming any other
// field is rejected at parse time (extract.go), never reaching the
// ledger.
type FactSubject string

const (
	// SubjectLanguages is the detected-language-set subject.
	SubjectLanguages FactSubject = "languages"
	// SubjectBuildCmd is the detected build-command subject.
	SubjectBuildCmd FactSubject = "build_cmd"
	// SubjectTestCmd is the detected test-command subject.
	SubjectTestCmd FactSubject = "test_cmd"
	// SubjectLintCmd is the detected lint-command subject.
	SubjectLintCmd FactSubject = "lint_cmd"
	// SubjectLayout is the repository layout-summary subject.
	SubjectLayout FactSubject = "layout"
	// SubjectCIPresence is the detected CI-system-presence subject.
	SubjectCIPresence FactSubject = "ci_presence"
	// SubjectHarnessFiles is the pre-existing AI-harness-files subject.
	SubjectHarnessFiles FactSubject = "harness_files"
)

// Valid reports whether s is one of the seven closed subjects. The zero
// value (empty string) is deliberately invalid.
func (s FactSubject) Valid() bool {
	switch s {
	case SubjectLanguages, SubjectBuildCmd, SubjectTestCmd, SubjectLintCmd,
		SubjectLayout, SubjectCIPresence, SubjectHarnessFiles:
		return true
	default:
		return false
	}
}

// FactState is one InferredFact's lifecycle state.
type FactState string

const (
	// FactProposed is the initial state every extract-intake record
	// starts in. A subject no detector covers and no human has accepted
	// stays here indefinitely -- a normal terminal state, not a failure.
	FactProposed FactState = "proposed"
	// FactAccepted means the record passed corroboration or was
	// human-accepted, and is eligible for policy projection.
	FactAccepted FactState = "accepted"
	// FactRejected means a deterministic detector contradicted the
	// proposed value, or schema validation failed.
	FactRejected FactState = "rejected"
	// FactSuperseded means a later accepted version replaced this one.
	FactSuperseded FactState = "superseded"
)

// Valid reports whether s is one of the four declared states.
func (s FactState) Valid() bool {
	switch s {
	case FactProposed, FactAccepted, FactRejected, FactSuperseded:
		return true
	default:
		return false
	}
}

// InferredFact is one record in the ledger, carrying EXACTLY the decided
// field set {fact, source, confidence, version, accepted_by, rollback}
// plus its identity and lifecycle state.
type InferredFact struct {
	// ID identifies this fact's version chain (stable across
	// supersede/rollback); Version distinguishes entries within it.
	ID string `json:"id"`
	// RepositoryID names the repository this fact was extracted from.
	RepositoryID string `json:"repository_id"`
	// Subject is the closed R-21.180 subject this fact classifies.
	Subject FactSubject `json:"subject"`
	// Fact is the extracted value itself, as free-form text (the
	// specific shape -- a command string, a language name, a bool
	// rendered as text -- is Subject-dependent and validated by
	// extract.go's schema check, not by this type).
	Fact string `json:"fact"`
	// Source is the provenance tag carried from extract.go's untrusted
	// tagging (F/S-10.T4): every extractor input is tagged untrusted
	// before the request is built, and that tag survives into this
	// field unchanged.
	Source string `json:"source"`
	// Confidence is the model's own confidence, in [0, 1].
	Confidence float64 `json:"confidence"`
	// Version is this record's position in its ID's version chain,
	// starting at 1. Accepting a new proposal for an already-accepted
	// subject supersedes the prior version and bumps this by one.
	Version int `json:"version"`
	// AcceptedBy is empty until State transitions to FactAccepted, then
	// holds the corroborating detector id or the human approver
	// identity for the CLI accept path.
	AcceptedBy string `json:"accepted_by,omitempty"`
	// Rollback is empty until a later version supersedes this one, then
	// holds the ID of the version this one can roll back to.
	Rollback string `json:"rollback,omitempty"`
	// State is the lifecycle state.
	State FactState `json:"state"`
	// RejectReason is set only when State is FactRejected, recording the
	// deterministic contradiction or schema-validation failure.
	RejectReason string `json:"reject_reason,omitempty"`
}

// Validate checks every field's closed-set and range membership. It does
// NOT check state-transition legality (ledger.go owns that) or
// corroboration (ledger.go owns that too) -- this is the schema-shape
// check extract.go's parse step and ledger.go's persistence both run
// before anything else.
func (f InferredFact) Validate() error {
	if f.ID == "" {
		return cascade.New(cascade.KindInvalidInput, "repo: inferred fact requires a non-empty id")
	}
	if f.RepositoryID == "" {
		return cascade.New(cascade.KindInvalidInput, "repo: inferred fact requires a non-empty repository id")
	}
	if !f.Subject.Valid() {
		return cascade.Newf(cascade.KindInvalidInput,
			"repo: %q is not a closed R-21.180 fact subject", string(f.Subject))
	}
	if f.Fact == "" {
		return cascade.New(cascade.KindInvalidInput, "repo: inferred fact requires a non-empty fact value")
	}
	if f.Source == "" {
		return cascade.New(cascade.KindInvalidInput, "repo: inferred fact requires a non-empty source tag")
	}
	if f.Confidence < 0 || f.Confidence > 1 {
		return cascade.Newf(cascade.KindInvalidInput,
			"repo: inferred fact confidence %v is outside [0,1]", f.Confidence)
	}
	if f.Version < 1 {
		return cascade.Newf(cascade.KindInvalidInput, "repo: inferred fact version %d must be >= 1", f.Version)
	}
	if !f.State.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "repo: %q is not a declared fact state", string(f.State))
	}
	return nil
}
