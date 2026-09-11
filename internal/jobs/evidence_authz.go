package jobs

// Purpose: R-21.145/R-21.177 producer authorization -- the ONLY path
//
//	that may authorize an EvidenceLedger.Append: the controller process
//	itself, presenting an execution id it actually spawned and a lease
//	epoch that still matches the AC/S-59.T2 fence -- plus R-21.183's
//	emitter-identity shape check, keyed off EvidenceKind rather than
//	ProducerCapability (see this file's requireIdentitySource doc
//	comment for why).
//
// Inputs: an AppendAuthorization {execution_id, lease repo/scope, lease
//
//	epoch}.
//
// Outputs: nil, or the typed ErrEvidenceProducerDenied -- no row is ever
//
//	written alongside a non-nil result (evidence.go calls Authorize
//	before its insert transaction opens).
//
// Constraints: isController is the SAME single-process-identity seam
//
//	LeaseManager already uses (lease.go's NewLeaseManager) -- there is
//	no per-row "spawned by" marker on the Execution record (model.go),
//	so "the controller itself spawned that execution" is, exactly like
//	the lease model, a PROCESS-IDENTITY check, not a per-row one; a
//	non-controller process (a node daemon, a delegated agent session)
//	never satisfies isController regardless of which execution id it
//	presents. No RPC method in this ticket's files_scope exposes any of
//	this -- that exclusion is the RPC layer's job (AD/S-62.T3,
//	AK/S-73.T3), out of scope here by files_scope; this file is the
//	authorization the RPC layer would otherwise be the only thing
//	standing in front of.
//
// SPORT: jobs/completion-gate/ADD (P1-E29-W6-S60-T3).

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// LeaseFencer is the AC/S-59.T2 fence this file presents its epoch to.
// *LeaseManager satisfies it (lease_fence.go's Fence method); the
// interface exists so evidence_authz_test.go can exercise the stale/
// wrong-scope refusals against a real *LeaseManager without this file
// importing anything beyond what it already does.
type LeaseFencer interface {
	Fence(ctx context.Context, repoID, scopeGlob string, epoch int64) error
}

// AppendAuthorization is the {execution_id, lease epoch} pair R-21.177
// requires on every Append.
type AppendAuthorization struct {
	ExecutionID    string
	LeaseRepoID    string
	LeaseScopeGlob string
	LeaseEpoch     int64
}

// ProducerAuthz is the R-21.145/R-21.177 producer-authorization check.
type ProducerAuthz struct {
	store        *Store
	isController func() bool
	fencer       LeaseFencer
}

// NewProducerAuthz constructs the check. fencer may be nil to disable
// the epoch fence (a store with no leases at all, e.g. certain test
// fixtures); a nil isController is treated as "never the controller"
// fail-closed, never as "always the controller".
func NewProducerAuthz(store *Store, isController func() bool, fencer LeaseFencer) *ProducerAuthz {
	return &ProducerAuthz{store: store, isController: isController, fencer: fencer}
}

// Authorize implements the four cases evidence_authz_test.go's
// TestEvidenceAuthz names: the controller-spawned accept, the
// foreign-execution refusal, the stale-epoch refusal, and (handled by
// completion.go's completeness check rather than here)
// agent-claim-never-satisfies-a-gate.
func (a *ProducerAuthz) Authorize(ctx context.Context, auth AppendAuthorization) error {
	if a == nil || a.store == nil {
		return cascade.New(cascade.KindInvalidInput, "jobs: nil ProducerAuthz")
	}
	if a.isController == nil || !a.isController() {
		return cascade.Wrap(cascade.KindPermissionDenied, ErrEvidenceProducerDenied, "jobs: caller is not the controller")
	}
	if auth.ExecutionID == "" {
		return cascade.Wrap(cascade.KindInvalidInput, ErrEvidenceProducerDenied, "jobs: evidence append requires an execution id")
	}
	if _, ok, err := a.store.GetExecution(ctx, auth.ExecutionID); err != nil {
		return err
	} else if !ok {
		return cascade.Wrapf(cascade.KindPermissionDenied, ErrEvidenceProducerDenied,
			"jobs: execution %q was not spawned by this controller", auth.ExecutionID)
	}
	if a.fencer != nil {
		if err := a.fencer.Fence(ctx, auth.LeaseRepoID, auth.LeaseScopeGlob, auth.LeaseEpoch); err != nil {
			return cascade.Wrapf(cascade.KindPermissionDenied, ErrEvidenceProducerDenied,
				"jobs: lease epoch %d for %s/%s does not match the holding lease", auth.LeaseEpoch, auth.LeaseRepoID, auth.LeaseScopeGlob)
		}
	}
	return nil
}

// The three identity-source prefixes R-21.183 names. attestor_identity
// carries exactly one of them, chosen by the caller but VALIDATED here
// against the shape its row's EvidenceKind requires -- never chosen by
// the caller in the sense of overriding which prefix a kind demands.
const (
	identityPrefixNode     = "node:"
	identityPrefixDaemon   = "daemon:"
	identityPrefixApproval = "approval:"
)

// requireIdentitySource enforces R-21.183's emitter-identity rule,
// keyed by EvidenceKind (the ruling names three concrete row shapes --
// "a ci_attestation row", "a policy-engine state transition" [i.e. the
// build/lint/tests/review/adversarial rows a Transition's own
// completeness check consumes], "a human_approval row" -- rather than
// by the four-member ProducerCapability set, which has no member
// spanning exactly those three kinds). ci_attestation requires the node
// key id prefix, human_approval requires the approval token id prefix,
// and every other kind requires the daemon key id prefix. A mismatch or
// an empty identity is ErrEvidenceProducerDenied; no row is written.
func requireIdentitySource(kind EvidenceKind, identity string) error {
	var want string
	switch kind {
	case EvidenceCIAttestation:
		want = identityPrefixNode
	case EvidenceHumanApproval:
		want = identityPrefixApproval
	case EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview, EvidenceAdversarial:
		want = identityPrefixDaemon
	default:
		want = identityPrefixDaemon
	}
	if identity == "" || !strings.HasPrefix(identity, want) {
		return cascade.Wrapf(cascade.KindPermissionDenied, ErrEvidenceProducerDenied,
			"jobs: evidence kind %q requires an attestor_identity prefixed %q", string(kind), want)
	}
	return nil
}
