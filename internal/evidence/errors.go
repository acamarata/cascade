package evidence

// Purpose: this package's typed sentinels, split into their own file per
// the R-16.47 errors-category 300-line-cap precedent (internal/jobs's own
// completion_errors.go/evidence_authz.go split). Each wraps its frozen
// A-T7 kind so the CLI exit and JSON-RPC code tables resolve without a
// new mapping.
//
// SPORT: evidence/claim-record (ADD).

import "github.com/acamarata/cascade/pkg/cascade"

var (
	// ErrClaimNotFound reports that no claim row exists for a given id.
	ErrClaimNotFound = cascade.New(cascade.KindNotFound, "evidence: claim not found")
	// ErrEvidenceNotFound reports that no evidence row exists for a given
	// id.
	ErrEvidenceNotFound = cascade.New(cascade.KindNotFound, "evidence: evidence record not found")
	// ErrClaimWithoutEvidence reports that a claim has zero
	// claim_evidence rows and is therefore never readable as
	// established.
	ErrClaimWithoutEvidence = cascade.New(cascade.KindInvalidInput, "evidence: claim has no evidence rows")
	// ErrInvalidLocator reports a malformed, inverted, negative-bound, or
	// foreign-scheme locator string.
	ErrInvalidLocator = cascade.New(cascade.KindInvalidInput, "evidence: invalid locator")
	// ErrClaimInvalidated reports that Invalidate was called a second
	// time on an already-invalidated claim.
	ErrClaimInvalidated = cascade.New(cascade.KindConflict, "evidence: claim already invalidated")
	// ErrDataClassImmutable reports that a write attempted to change a
	// stored row's data_class.
	ErrDataClassImmutable = cascade.New(cascade.KindConflict, "evidence: data_class is immutable")
	// ErrContentHashMismatch reports that fetched or captured bytes no
	// longer match their recorded content_hash.
	ErrContentHashMismatch = cascade.New(cascade.KindIntegrity, "evidence: content hash mismatch")
	// ErrEvidenceStale reports that a git-sourced evidence row's recorded
	// commit:path no longer contains the recorded bytes.
	ErrEvidenceStale = cascade.New(cascade.KindIntegrity, "evidence: evidence is stale")
)
