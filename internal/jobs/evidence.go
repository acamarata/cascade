package jobs

// Purpose: the R-21.145/R-21.183 per-job evidence ledger -- the seven
//
//	EvidenceKind values, the closed ProducerCapability set, the typed
//	EvidenceRecord row, and EvidenceLedger's Append/Query/Cursor/Verify
//	over the jobs_evidence table (migration_evidence.go). The ledger is
//	the ONLY place "an agent said done" becomes a row: an agent-claim
//	row is recorded evidence OF A CLAIM and never satisfies a gate by
//	itself (evidence_authz.go's Authorize call happens before Append
//	ever writes a row; completion.go's completeness check skips
//	agent-claim rows entirely).
//
// Inputs: an EvidenceRecord plus an AppendAuthorization (evidence_authz.go).
// Outputs: the persisted record with its assigned Seq/PrevHash, or a
//
//	typed refusal; Query's latest pass/fail row per kind; Cursor's max
//	seq; Verify's chain walk result.
//
// Constraints: append-only (no UPDATE/DELETE path exists at all --
//
//	there is no method that could issue one); a repeated idempotency_key
//	is a no-op returning the existing row; every timestamp comes from
//	the injected runtime.Clock (Art.7.3); every Append is audited
//	through the injected audit.Writer (I/S-18.T2) BEFORE the caller sees
//	success, so a write that was never audited never landed either.
//
// SPORT: jobs/completion-gate/ADD (P1-E29-W6-S60-T3).

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// EvidenceKind is the closed, seven-member evidence vocabulary. The zero
// value is deliberately not a member (R-16.37 no-permissive-zero rule).
type EvidenceKind string

const (
	// EvidenceBuild is a passing build's evidence row.
	EvidenceBuild EvidenceKind = "build"
	// EvidenceLint is a passing lint run's evidence row.
	EvidenceLint EvidenceKind = "lint"
	// EvidenceTests is a passing test run's evidence row.
	EvidenceTests EvidenceKind = "tests"
	// EvidenceReview is a passing code review's evidence row.
	EvidenceReview EvidenceKind = "review"
	// EvidenceAdversarial is a passing adversarial review's evidence row.
	EvidenceAdversarial EvidenceKind = "adversarial"
	// EvidenceCIAttestation is a real CI attestation's evidence row.
	EvidenceCIAttestation EvidenceKind = "ci_attestation"
	// EvidenceHumanApproval is a Critical-class human approval's row.
	EvidenceHumanApproval EvidenceKind = "human_approval"
)

// Valid reports whether k is one of the seven closed members.
func (k EvidenceKind) Valid() bool {
	switch k {
	case EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview,
		EvidenceAdversarial, EvidenceCIAttestation, EvidenceHumanApproval:
		return true
	}
	return false
}

// ProducerCapability is the closed, four-member set of principals that
// may ever appear on an evidence row's producer_capability column.
type ProducerCapability string

const (
	// ProducerControllerRun is a controller-executed check's row.
	ProducerControllerRun ProducerCapability = "controller-run"
	// ProducerAttestor is a node's signed CI attestation row.
	ProducerAttestor ProducerCapability = "attestor"
	// ProducerHumanApproval is a human decision's row.
	ProducerHumanApproval ProducerCapability = "human-approval"
	// ProducerAgentClaim is an agent's own claim -- never satisfies a
	// gate alone.
	ProducerAgentClaim ProducerCapability = "agent-claim"
)

// Valid reports whether c is one of the four closed members.
func (c ProducerCapability) Valid() bool {
	switch c {
	case ProducerControllerRun, ProducerAttestor, ProducerHumanApproval, ProducerAgentClaim:
		return true
	}
	return false
}

// Outcome is the closed, three-member evidence outcome.
type Outcome string

const (
	// OutcomePass is a satisfied check.
	OutcomePass Outcome = "pass"
	// OutcomeFail is a failed check.
	OutcomeFail Outcome = "fail"
	// OutcomePending is a check still running.
	OutcomePending Outcome = "pending"
)

// Valid reports whether o is one of the three closed members.
func (o Outcome) Valid() bool {
	switch o {
	case OutcomePass, OutcomeFail, OutcomePending:
		return true
	}
	return false
}

// genesisHash is the R-21.183 32-zero-byte genesis prev_hash value for
// the first row of any job's chain.
var genesisHash = make([]byte, sha256.Size)

// EvidenceRecord is one typed, complete evidence row (R-21.145). No
// free-form field exists anywhere on this type.
type EvidenceRecord struct {
	JobID              string
	Seq                int64 // assigned by Append; ignored on input
	Kind               EvidenceKind
	ProducerCapability ProducerCapability
	AttemptID          string
	CheckpointID       string
	TreeHash           string
	AttestorIdentity   string
	PrevHash           []byte // assigned by Append; ignored on input
	StartedAt          time.Time
	EndedAt            time.Time
	Outcome            Outcome
	ResultHash         string
	IdempotencyKey     string
	ArtifactRef        string
	CreatedAt          time.Time // assigned by Append; ignored on input
}

// canonicalJSON is the exact wire shape prevHash chains over: field
// order fixed by this struct's declaration, never the Go struct's own
// (unexported fields excluded by construction -- there are none here).
type canonicalRow struct {
	JobID              string `json:"job_id"`
	Seq                int64  `json:"seq"`
	Kind               string `json:"kind"`
	ProducerCapability string `json:"producer_capability"`
	AttemptID          string `json:"attempt_id"`
	CheckpointID       string `json:"checkpoint_id"`
	TreeHash           string `json:"tree_hash"`
	AttestorIdentity   string `json:"attestor_identity"`
	StartedAtUnix      int64  `json:"started_at"`
	EndedAtUnix        int64  `json:"ended_at"`
	Outcome            string `json:"outcome"`
	ResultHash         string `json:"result_hash"`
	IdempotencyKey     string `json:"idempotency_key"`
	ArtifactRef        string `json:"artifact_ref"`
}

func canonicalize(r EvidenceRecord) ([]byte, error) {
	row := canonicalRow{
		JobID: r.JobID, Seq: r.Seq, Kind: string(r.Kind),
		ProducerCapability: string(r.ProducerCapability), AttemptID: r.AttemptID,
		CheckpointID: r.CheckpointID, TreeHash: r.TreeHash, AttestorIdentity: r.AttestorIdentity,
		StartedAtUnix: r.StartedAt.Unix(), EndedAtUnix: r.EndedAt.Unix(),
		Outcome: string(r.Outcome), ResultHash: r.ResultHash,
		IdempotencyKey: r.IdempotencyKey, ArtifactRef: r.ArtifactRef,
	}
	return json.Marshal(row)
}

func chainHash(r EvidenceRecord) ([]byte, error) {
	raw, err := canonicalize(r)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "jobs: canonicalize evidence row")
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}

// EvidenceLedger is the per-job append-only evidence ledger, backed by
// the jobs_evidence table.
type EvidenceLedger struct {
	store  *Store
	clock  runtime.Clock
	writer audit.Writer
	authz  *ProducerAuthz
}

// NewEvidenceLedger constructs a ledger. writer is REQUIRED -- an
// EvidenceLedger with no audit sink would let a write land unaudited,
// which R-16.47 forbids outright, so this constructor refuses a nil
// writer rather than silently degrading (unlike leaseEventSink's
// deliberately-optional fan-outs, which describe telemetry, not the
// R-16.47 audit requirement itself). authz may be nil only in tests that
// exercise Append's other refusals directly against a pre-built
// AppendAuthorization; production always wires a real *ProducerAuthz.
func NewEvidenceLedger(store *Store, clock runtime.Clock, writer audit.Writer, authz *ProducerAuthz) (*EvidenceLedger, error) {
	if store == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "jobs: EvidenceLedger requires a non-nil store")
	}
	if clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "jobs: EvidenceLedger requires a non-nil Clock")
	}
	if writer == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "jobs: EvidenceLedger requires a non-nil audit.Writer")
	}
	return &EvidenceLedger{store: store, clock: clock, writer: writer, authz: authz}, nil
}

// validateEvidenceRecord runs Append's field-level checks: the closed
// vocabularies, the required idempotency key, and the R-21.183
// emitter-identity shape.
func validateEvidenceRecord(rec EvidenceRecord) error {
	if rec.JobID == "" {
		return cascade.New(cascade.KindInvalidInput, "jobs: evidence record requires a job id")
	}
	if !rec.Kind.Valid() {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownEvidenceKind, "jobs: %q", string(rec.Kind))
	}
	if !rec.ProducerCapability.Valid() {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownProducerCapability, "jobs: %q", string(rec.ProducerCapability))
	}
	if !rec.Outcome.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "jobs: unknown evidence outcome %q", string(rec.Outcome))
	}
	if rec.IdempotencyKey == "" {
		return cascade.New(cascade.KindInvalidInput, "jobs: evidence record requires an idempotency key")
	}
	return requireIdentitySource(rec.Kind, rec.AttestorIdentity)
}

// Append validates rec, authorizes the write against auth (evidence_authz.go),
// computes the R-21.183 hash-chain link and appends exactly one row --
// or, for a repeated idempotency_key, returns the EXISTING row as a
// no-op. Every successful Append (including the no-op) is preceded by a
// real audit.Writer.Append call.
func (l *EvidenceLedger) Append(ctx context.Context, rec EvidenceRecord, auth AppendAuthorization) (EvidenceRecord, error) {
	if l == nil {
		return EvidenceRecord{}, cascade.New(cascade.KindInvalidInput, "jobs: nil EvidenceLedger")
	}
	if err := validateEvidenceRecord(rec); err != nil {
		return EvidenceRecord{}, err
	}
	if l.authz != nil {
		if err := l.authz.Authorize(ctx, auth); err != nil {
			return EvidenceRecord{}, err
		}
	}

	var out EvidenceRecord
	txErr := l.store.withTx(ctx, func(tx *sql.Tx) error {
		if existing, ok, err := existingByIdempotencyKeyTx(ctx, tx, rec.JobID, rec.IdempotencyKey); err != nil {
			return err
		} else if ok {
			out = existing
			return nil
		}
		prev, err := prevHashTx(ctx, tx, rec.JobID)
		if err != nil {
			return err
		}
		seq, err := nextSeqTx(ctx, tx, rec.JobID)
		if err != nil {
			return err
		}
		now := l.clock.Now().UTC()
		rec.Seq = seq
		rec.CreatedAt = now
		rec.PrevHash = prev
		if err := insertEvidenceRowTx(ctx, tx, rec); err != nil {
			return err
		}
		out = rec
		return nil
	})
	if txErr != nil {
		return EvidenceRecord{}, txErr
	}

	if _, err := l.writer.Append(ctx, audit.Event{
		Kind: audit.KindPolicyDecide, Actor: "jobs.evidence",
		Action: "evidence.append:" + string(out.Kind), Outcome: string(out.Outcome),
	}); err != nil {
		return EvidenceRecord{}, err
	}
	return out, nil
}

// Query and Cursor are defined in migration_evidence.go, alongside the
// other row-level SQL this file's Append shares with them.
