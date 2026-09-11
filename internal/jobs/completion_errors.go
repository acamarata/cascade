package jobs

// Purpose: the completion gate's error-category file (R-16.47's "errors"
//
//	category: every typed sentinel and denial-error type for the
//	completion gate lives together, separate from evidence.go's
//	records/ledger and completion.go's policy engine, per the 300-line
//	split).
//
// Inputs: none (types/values only).
// Outputs: sentinels for evidence.go/evidence_authz.go's typed refusals,
//
//	plus ErrorCompletionDenied, completion.go's explain-why denial type.
//
// Constraints: every sentinel wraps exactly one frozen pkg/cascade.Kind
//
//	(R-14.2); ErrorCompletionDenied.Error() names every populated field,
//	never a generic "denied" message.
//
// SPORT: jobs/completion-gate/ADD (P1-E29-W6-S60-T3).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ApprovalScopeAction encodes {job_id, tree_hash, policy_version} as the
// fixed-length (64 hex chars, always <= the approval queue's own 64-char
// display sanitization ceiling -- approval_queue_enqueue.go's
// validateApprovalText) hex-SHA256 digest of their canonical JSON, which
// ApprovalQueue.ConsumeToken re-derives from the action ABOUT TO RUN and
// compares. A raw JSON encoding of a real tree hash (itself typically 64
// hex chars) would exceed that ceiling; hashing keeps the binding exact
// while staying inside it. Exported so a real caller minting the token
// at admission time (outside this ticket's files_scope) uses the
// identical Action.
func ApprovalScopeAction(jobID, treeHash, policyVersion string) string {
	raw, _ := json.Marshal(approvalScope{JobID: jobID, TreeHash: treeHash, PolicyVersion: policyVersion})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// evidenceKindsForGateSet maps a RiskClass's GateItem set to the
// EvidenceKinds completion.go's completeness check requires a passing
// row for. Only five of the seven EvidenceKinds have a corresponding
// GateItem in riskgates.go's table (GateHumanApproval maps to the
// separate Critical-class approval check, not to completeness; nothing
// maps to EvidenceCIAttestation until AF/S-65.T4 wires it) -- this is a
// documented gap-filling mapping, same posture as model.go's own
// DataClass/EvidenceKind precedent, since no ticket owns a GateItem->
// EvidenceKind table yet.
func evidenceKindsForGateSet(gates GateSet) []EvidenceKind {
	has := make(map[GateItem]bool, len(gates))
	for _, g := range gates {
		has[g] = true
	}
	var out []EvidenceKind
	if has[GateBuild] {
		out = append(out, EvidenceBuild)
	}
	if has[GateLint] {
		out = append(out, EvidenceLint)
	}
	if has[GateTargetedTests] {
		out = append(out, EvidenceTests)
	}
	if has[GateCodeReview] {
		out = append(out, EvidenceReview)
	}
	if has[GateAdversarialReview] {
		out = append(out, EvidenceAdversarial)
	}
	return out
}

// requiresHumanApproval reports whether target's Critical-class gate set
// includes GateHumanApproval.
func requiresHumanApproval(class RiskClass, target JobState) bool {
	if target != JobStateAccepted {
		return false
	}
	gates, err := GateSetForRiskClass(class)
	if err != nil {
		return false
	}
	for _, g := range gates {
		if g == GateHumanApproval {
			return true
		}
	}
	return false
}

// gateDeniedWirePayload is the R-16.73 jobs.gate.denied wire shape.
type gateDeniedWirePayload struct {
	JobID     string `json:"job_id"`
	SessionID string `json:"session_id"`
	TicketID  string `json:"ticket_id"`
	Reason    string `json:"reason"`
	Attempt   int    `json:"attempt"`
}

// approvalScope is the R-21.164 scope a human_approval token binds to,
// encoded as the ApprovalQueue.ConsumeToken Action string.
type approvalScope struct {
	JobID         string `json:"job_id"`
	TreeHash      string `json:"tree_hash"`
	PolicyVersion string `json:"policy_version"`
}

// Sentinels this ticket's contract names.
var (
	// ErrNotPolicyEngine is returned by CompletionPolicy.Transition when
	// the caller does not present the real policy-engine identity.
	ErrNotPolicyEngine = cascade.New(cascade.KindPermissionDenied, "jobs: caller is not the policy engine")
	// ErrNoEvidence is returned by EvidenceLedger.Query when jobID has no
	// record of the requested kind.
	ErrNoEvidence = cascade.New(cascade.KindNotFound, "jobs: no evidence record for that kind")
	// ErrUnknownEvidenceKind is returned for an EvidenceKind outside the
	// closed seven-member set.
	ErrUnknownEvidenceKind = cascade.New(cascade.KindInvalidInput, "jobs: unknown evidence kind")
	// ErrUnknownProducerCapability is ErrUnknownEvidenceKind's sibling
	// for ProducerCapability.
	ErrUnknownProducerCapability = cascade.New(cascade.KindInvalidInput, "jobs: unknown producer capability")
	// ErrEvidenceProducerDenied is returned when an Append's presented
	// {execution_id, lease epoch} does not authorize the write, or when
	// attestor_identity's shape does not match the source its
	// producer_capability requires. No row is ever written alongside
	// this error.
	ErrEvidenceProducerDenied = cascade.New(cascade.KindPermissionDenied, "jobs: evidence producer denied")
	// ErrEvidenceChainBroken is returned by EvidenceLedger.Verify (and by
	// CompletionPolicy.Transition, which calls Verify before its
	// completeness check) on a tampered row, a seq gap, or a bad genesis
	// value.
	ErrEvidenceChainBroken = cascade.New(cascade.KindIntegrity, "jobs: evidence hash chain broken")
)

// ErrorCompletionDenied is CompletionPolicy.Transition's typed denial:
// at least one field is always populated, and Error() names every one
// that is.
type ErrorCompletionDenied struct {
	// MissingEvidence lists the EvidenceKinds the risk-class gate set
	// required that had no passing record.
	MissingEvidence []EvidenceKind
	// WrongSuccessor is non-nil when target was not the legal successor
	// of the job's current state.
	WrongSuccessor *JobState
	// ExpiredApproval is true when the Critical-class human_approval
	// check failed for any reason (absent, wrong scope, consumed, or
	// expired) -- the reason names which.
	ExpiredApproval bool
	// CheckpointMismatch names the checkpoint id the evidence rows carry
	// when it differs from the one Transition was evaluated against.
	CheckpointMismatch *string
	// RiskEscalated is non-nil when the AC/S-59.T4 Reclassify entry
	// point derived a class above the job's planned class.
	RiskEscalated *Escalation
	// OutOfLeaseScope lists the changed paths outside the holding
	// lease's normalized scope prefixes.
	OutOfLeaseScope []string
	// Reason is a free-text explanation for the one case above with no
	// structured payload of its own (an expired/absent/foreign approval
	// token) -- named so ExpiredApproval never denies silently.
	Reason string
}

// Error implements the error interface, naming every populated field.
func (e *ErrorCompletionDenied) Error() string {
	var parts []string
	if len(e.MissingEvidence) > 0 {
		kinds := make([]string, len(e.MissingEvidence))
		for i, k := range e.MissingEvidence {
			kinds[i] = string(k)
		}
		parts = append(parts, "missing evidence: "+strings.Join(kinds, ","))
	}
	if e.WrongSuccessor != nil {
		parts = append(parts, fmt.Sprintf("wrong successor: %s", *e.WrongSuccessor))
	}
	if e.ExpiredApproval {
		reason := e.Reason
		if reason == "" {
			reason = "human approval missing or invalid"
		}
		parts = append(parts, "expired/invalid approval: "+reason)
	}
	if e.CheckpointMismatch != nil {
		parts = append(parts, "checkpoint mismatch: evidence carries "+*e.CheckpointMismatch)
	}
	if e.RiskEscalated != nil {
		parts = append(parts, fmt.Sprintf("risk escalated: %s -> %s", e.RiskEscalated.From, e.RiskEscalated.To))
	}
	if len(e.OutOfLeaseScope) > 0 {
		parts = append(parts, "out of lease scope: "+strings.Join(e.OutOfLeaseScope, ","))
	}
	if len(parts) == 0 {
		parts = append(parts, "denied")
	}
	return "jobs: completion denied (" + strings.Join(parts, "; ") + ")"
}
