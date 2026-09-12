package jobs

// Purpose: the Critical-class release/CD gate (19 Epic AH DECIDED
//
//	R-16.13: "Release/CD gate = Critical class: explicit human approval +
//	rollback evidence"; R-16.70(b)'s ONE risk model already puts
//	GateHumanApproval/GateRollbackEvidence/GateReleaseGate on
//	riskgates.go's RiskCritical row -- this file is the first template
//	whose completion actually requires that triple). RequireHumanApproval
//	drives the real I/S-18.T3 ApprovalQueue (Enqueue/GetPending/
//	ConsumeToken) to redeem a human-issued, candidate-bound approval token
//	and pairs it with a verified RollbackEvidence artifact
//	(rollback_evidence.go), returning the EvidenceRecord
//	CompletionPolicy.Transition's own Critical-class check
//	(completion.go, out of this ticket's files_scope) consumes.
//
// Inputs: a policy.ApprovalQueue, the DagNode the release job resolved
//
//	to, a ReleaseCandidate naming what is being released, and a
//	RollbackEvidence.
//
// Outputs: a human_approval EvidenceRecord whose ArtifactRef points at
//	the verified rollback-evidence artifact, or one of four typed
//	refusals -- never an implicit approval.
//
// Constraints: FAIL CLOSED throughout. The approval token is bound to
//
//	{commit_sha, tree_hash, gate_set_version, job_id} (R-21.194, which
//	SUPERSEDES this ticket's own earlier free-form-scope draft with
//	R-21.164's {job_id, tree_hash, policy_version} triple -- see this
//	file's CONTRACT DEVIATION note below for why a second, four-field
//	scope-action function exists alongside completion_errors.go's
//	pre-existing three-field ApprovalScopeAction). A scope mismatch is
//	ErrApprovalScopeMismatch; a non-human issuing principal is
//	ErrNonHumanApprovalPrincipal; any other ApprovalQueue refusal
//	(including ErrTokenExpired/ErrTokenReplayed, propagated wrapped, never
//	swallowed) is ErrReleaseApprovalMissing; unverified rollback evidence
//	is ErrMissingRollbackEvidence (rollback_evidence.go). No path returns
//	a populated EvidenceRecord alongside a non-nil error, and no path
//	defaults to an implicit approval on any collaborator error.
//
// CONTRACT DEVIATION (R-16.79 -- files_scope/contract text is intent, not
// proof of the real tree): this ticket's contract names ReleaseTemplate's
// fields as {approvalQueue ApprovalQueue, rollback RollbackEvidence}, but
// its own HOW-1 Resolve description produces only FIXED and pass-through
// DagNode fields that never read either one -- JobTemplate.Resolve
// (template.go) takes a single ctx argument, and a release gate's
// DagNode shape does not vary by which ApprovalQueue or RollbackEvidence
// a future caller will supply at evaluation time (those belong to the
// separate RequireHumanApproval call this same ticket specifies with its
// own explicit parameters). Storing unread fields on ReleaseTemplate
// would be dead weight the tree gives no seam to consume; template_kinds.go
// therefore declares ReleaseTemplate as a stateless struct, matching the
// existing read-only kinds (ReviewTemplate/AdversarialTemplate/QaTemplate),
// and RequireHumanApproval takes approvalQueue and rb as direct parameters
// exactly as this ticket's own HOW-3 signature already specifies. Also:
// the contract's step-4 EvidenceRecord literal names a "Source" field:
// EvidenceRecord (evidence.go, AC/S-60.T3, already forged, out of this
// ticket's files_scope) has no such field. The real, equivalent field is
// AttestorIdentity, and evidence_authz.go's requireIdentitySource requires
// it prefixed "approval:" for EvidenceKindHumanApproval (identityPrefixApproval),
// not the contract's "human:<actor>" spelling -- this file follows the
// tree's real required prefix.
//
// SPORT: jobs/release-gate/ADD (P1-E34-W7-S70-T2).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// releaseGateCapability is the capability name a release-gate approval is
// enqueued under.
const releaseGateCapability = "jobs.release-gate"

// Sentinels this ticket's contract names. Each carries a DIFFERENT
// pkg/cascade Kind so a caller (and this file's own tests) can tell them
// apart with errors.Is: cascade.Error.Is compares Kind alone (pkg/cascade's
// frozen taxonomy has no per-sentinel identity), so two of these sharing a
// Kind would be indistinguishable -- the same reasoning
// internal/policy/approval_queue_errors.go's own header comment gives for
// why ITS refusals need a Code, not just a Kind.
var (
	// ErrApprovalScopeMismatch is returned when the redeemed token's
	// scope does not equal {commit_sha, tree_hash, gate_set_version,
	// job_id} of the candidate being released -- including a token
	// issued for an earlier commit being replayed against a later one.
	ErrApprovalScopeMismatch = cascade.New(cascade.KindConflict, "jobs: release approval token scope mismatch")
	// ErrNonHumanApprovalPrincipal is returned when the redeemed token's
	// subject is not a human principal (policy.SubjectUser). No autonomy
	// preset can satisfy this: every non-user Subject kind (agent,
	// plugin -- the real policy.SubjectKind vocabulary; the contract's
	// "driver"/"node"/"daemon" spellings name no real SubjectKind and are
	// treated as non-human by the same "not SubjectUser" test) refuses.
	ErrNonHumanApprovalPrincipal = cascade.New(cascade.KindPermissionDenied, "jobs: release approval requires a human principal")
	// ErrReleaseApprovalMissing is returned for any other ApprovalQueue
	// refusal (expired, replayed, not yet approved, unknown request,
	// canceled, ...) rather than defaulting to an implicit approval. The
	// underlying error is always wrapped, never swallowed.
	ErrReleaseApprovalMissing = cascade.New(cascade.KindPolicyDenied, "jobs: release approval missing")
)

// ReleaseCandidate identifies the artifact a release/CD gate evaluates:
// the four values the approval token binds to (R-21.194), plus the
// requesting principal and the instant the gate node was reached (the
// reference VerifyRollbackEvidence's produced-before-the-gate check uses).
type ReleaseCandidate struct {
	// JobID is the release job's id.
	JobID string
	// CommitSHA is the commit being released.
	CommitSHA string
	// TreeHash is the tree hash being released.
	TreeHash string
	// GateSetVersion names the gate-set table version this candidate was
	// evaluated under.
	GateSetVersion string
	// Requester is the principal on whose behalf the approval is asked.
	// RequireHumanApproval does not trust this field for its human-only
	// check (a caller could set anything here); it validates the REAL
	// subject the queue returns from ConsumeToken instead.
	Requester policy.Subject
	// GateReachedAt is when this release job's gate node was evaluated --
	// the reference instant a RollbackEvidence's ProducedAt must precede.
	GateReachedAt time.Time
}

// validate refuses a candidate missing any of the four scope-bound
// fields, an invalid Requester, or a zero GateReachedAt -- a caller must
// supply every value this file's binding and evidence checks depend on.
func (c ReleaseCandidate) validate() error {
	switch {
	case c.JobID == "":
		return cascade.New(cascade.KindInvalidInput, "jobs: release candidate requires a job id")
	case c.CommitSHA == "":
		return cascade.New(cascade.KindInvalidInput, "jobs: release candidate requires a commit sha")
	case c.TreeHash == "":
		return cascade.New(cascade.KindInvalidInput, "jobs: release candidate requires a tree hash")
	case c.GateSetVersion == "":
		return cascade.New(cascade.KindInvalidInput, "jobs: release candidate requires a gate set version")
	case c.GateReachedAt.IsZero():
		return cascade.New(cascade.KindInvalidInput, "jobs: release candidate requires gate_reached_at")
	}
	if err := c.Requester.Validate(); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "jobs: release candidate requester")
	}
	return nil
}

// releaseApprovalScope is the R-21.194 four-field scope a release
// approval token binds to, encoded as the ApprovalQueue's Action string
// (re-hashed and compared at ConsumeToken time, the same binding
// mechanism completion_errors.go's own three-field ApprovalScopeAction
// uses for R-21.164's older form -- see this file's header CONTRACT
// DEVIATION note for why a second function exists rather than changing
// that one, which is outside this ticket's files_scope).
type releaseApprovalScope struct {
	CommitSHA      string `json:"commit_sha"`
	TreeHash       string `json:"tree_hash"`
	GateSetVersion string `json:"gate_set_version"`
	JobID          string `json:"job_id"`
}

// releaseApprovalScopeAction hex-encodes the SHA-256 of cand's canonical
// four-field scope. Any of the four differing yields a different digest,
// which is what makes a token issued for one candidate unusable for
// another when ConsumeToken re-derives and compares it.
func releaseApprovalScopeAction(cand ReleaseCandidate) string {
	raw, _ := json.Marshal(releaseApprovalScope{
		CommitSHA: cand.CommitSHA, TreeHash: cand.TreeHash,
		GateSetVersion: cand.GateSetVersion, JobID: cand.JobID,
	})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// releaseGateSummary is the human-readable string a surface displays for
// a release-gate approval request.
func releaseGateSummary(cand ReleaseCandidate) string {
	return fmt.Sprintf("release gate approval for job %s at commit %s (gate-set %s)",
		cand.JobID, cand.CommitSHA, cand.GateSetVersion)
}

// releaseRefusal wraps cause as sentinel's own Kind, preserving cause on
// the Unwrap chain so errors.Is against the real policy sentinel (e.g.
// policy.ErrTokenExpired, policy.ErrTokenReplayed) still succeeds
// alongside errors.Is against sentinel itself (cascade.Error.Is compares
// Kind, and the returned value's Kind equals sentinel.Kind by
// construction).
func releaseRefusal(sentinel *cascade.Error, cause error, format string, args ...any) error {
	return cascade.Wrapf(sentinel.Kind, cause, format, args...)
}

// releaseApprovalRefusal wraps any ApprovalQueue error other than a scope
// mismatch as ErrReleaseApprovalMissing.
func releaseApprovalRefusal(jobID string, err error) error {
	return releaseRefusal(ErrReleaseApprovalMissing, err, "jobs: release approval missing for job %s", jobID)
}

// requireReleasePreconditions runs every check that must pass BEFORE the
// approval queue is ever touched: a non-nil queue, a Critical-class node
// (Epic AH's DECIDED rule, enforced here rather than trusted), a
// well-formed candidate, and verified rollback evidence.
func requireReleasePreconditions(approvalQueue policy.ApprovalQueue, node DagNode, cand ReleaseCandidate, rb RollbackEvidence) error {
	if approvalQueue == nil {
		return cascade.New(cascade.KindInvalidInput, "jobs: release gate requires a non-nil approval queue")
	}
	if node.RiskClass != RiskClassCritical {
		return cascade.Newf(cascade.KindInvalidInput,
			"jobs: release gate requires a Critical-class node, got %q", string(node.RiskClass))
	}
	if err := cand.validate(); err != nil {
		return err
	}
	return VerifyRollbackEvidence(rb, cand)
}

// redeemReleaseApproval drives Enqueue, GetPending and ConsumeToken in
// order and returns the redeemed ConsumeResult, or one of this file's
// typed refusals. It never reimplements the queue's own batching, dedup,
// expiry or ledger logic.
func redeemReleaseApproval(ctx context.Context, approvalQueue policy.ApprovalQueue, cand ReleaseCandidate, action string) (policy.ConsumeResult, error) {
	enqRes, err := approvalQueue.Enqueue(ctx, policy.EnqueueRequest{
		Subject:    cand.Requester,
		Capability: releaseGateCapability,
		Level:      policy.L3,
		Action:     action,
		Summary:    releaseGateSummary(cand),
	})
	if err != nil {
		return policy.ConsumeResult{}, releaseApprovalRefusal(cand.JobID, err)
	}

	if _, err := approvalQueue.GetPending(ctx); err != nil {
		return policy.ConsumeResult{}, releaseApprovalRefusal(cand.JobID, err)
	}

	consumeRes, err := approvalQueue.ConsumeToken(ctx, policy.ConsumeRequest{
		RequestID: enqRes.RequestID,
		Nonce:     enqRes.Token.Nonce,
		Action:    action,
	})
	if err != nil {
		if errors.Is(err, policy.ErrApprovalMismatch) {
			return policy.ConsumeResult{}, releaseRefusal(ErrApprovalScopeMismatch, err,
				"jobs: release approval token scope mismatch for job %s", cand.JobID)
		}
		return policy.ConsumeResult{}, releaseApprovalRefusal(cand.JobID, err)
	}
	return consumeRes, nil
}

// releaseApprovalEvidence builds the human_approval EvidenceRecord a
// successful redemption produces, its ArtifactRef naming the verified
// rollback-evidence artifact rather than an empty or free-form string.
func releaseApprovalEvidence(cand ReleaseCandidate, rb RollbackEvidence, action string, consumeRes policy.ConsumeResult) EvidenceRecord {
	return EvidenceRecord{
		JobID:              cand.JobID,
		Kind:               EvidenceHumanApproval,
		ProducerCapability: ProducerHumanApproval,
		AttestorIdentity:   "approval:" + consumeRes.Subject.ID,
		TreeHash:           cand.TreeHash,
		StartedAt:          consumeRes.ConsumedAt,
		EndedAt:            consumeRes.ConsumedAt,
		Outcome:            OutcomePass,
		IdempotencyKey:     action,
		ArtifactRef:        rollbackEvidenceArtifactRef(rb),
	}
}

// RequireHumanApproval drives the real I/S-18.T3 ApprovalQueue to redeem
// a human-issued, candidate-bound approval token for cand, verifies rb
// against cand, and returns the resulting human_approval EvidenceRecord.
// Every refusal is one of this file's typed sentinels, never an implicit
// approval.
func RequireHumanApproval(ctx context.Context, approvalQueue policy.ApprovalQueue, node DagNode, cand ReleaseCandidate, rb RollbackEvidence) (EvidenceRecord, error) {
	if err := requireReleasePreconditions(approvalQueue, node, cand, rb); err != nil {
		return EvidenceRecord{}, err
	}

	action := releaseApprovalScopeAction(cand)
	consumeRes, err := redeemReleaseApproval(ctx, approvalQueue, cand, action)
	if err != nil {
		return EvidenceRecord{}, err
	}

	if consumeRes.Subject.Kind != policy.SubjectUser {
		return EvidenceRecord{}, cascade.Wrapf(cascade.KindPermissionDenied, ErrNonHumanApprovalPrincipal,
			"jobs: release approval principal kind %q is not human", consumeRes.Subject.Kind)
	}

	return releaseApprovalEvidence(cand, rb, action, consumeRes), nil
}
