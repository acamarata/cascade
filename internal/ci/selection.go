// Purpose (this file): the R-21.173 target-selection decision --
// TargetSelection, AffectedStatus, and RequirementModel.SelectTargets,
// the ONE selection decision in the tree. Streaming CI (AF/S-65.T2) may
// dispatch against a partial affected set; acceptance must re-run the
// FULL requirement model whenever that set is uncomputable, stale, or
// the risk class is High/Critical -- this file owns that decision
// function, never re-implemented by a consumer.
//
// Inputs: a changed-path list, the AF/S-65.T2 immutable candidate
// snapshot's tree hash (R-21.147), and the AC/S-59.T4 risk classifier's
// output as a plain string (RiskClass stays a plain string here, not
// internal/jobs.RiskClass: this package must not import internal/jobs --
// SelectTargets only ever compares it against the two literal values
// "high"/"critical" that RiskClassHigh/RiskClassCritical already
// serialize to).
// Outputs: a CIRequirementPlan whose Selection/Targets pair is always
// self-consistent: Selection=full always carries []Target{TargetAll},
// Selection=affected always carries the RequirementModel.Affected's own
// (non-TargetAll) result.
// Constraints: fail-closed zero-value (06 §5.20) -- TargetSelection("")
// and AffectedStatus("") are not "affected"/"ok", they are "full"/
// "uncomputable" (Resolved below). candidateTreeHash's own identity is
// verified via a real `git rev-parse HEAD^{tree}` subprocess against
// m.WorktreeRoot (recorded design decision: R-16.71's Affected signature
// carries no tree-hash return, and this ticket does not depend on
// AF/S-65.T2, so SelectTargets treats candidateTreeHash as an opaque
// token to compare against the worktree's OWN current content-addressed
// tree identity rather than reaching into S-65.T2's internal snapshot
// format -- see this ticket's own build report for the full reasoning).
// SPORT: internal.ci.TargetSelection/ADDED, internal.ci.AffectedStatus/ADDED,
//
//	internal.ci.SelectTargets/ADDED (P1-E32-W6-S65-T1).

package ci

import (
	"bytes"
	"context"
	"os/exec"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TargetSelection is R-21.173's closed two-member outcome: whether a
// CIRequirementPlan carries a concrete affected set or the TargetAll
// fallback.
type TargetSelection string

// The closed two-member TargetSelection vocabulary.
const (
	TargetSelectionAffected TargetSelection = "affected"
	TargetSelectionFull     TargetSelection = "full"
)

// Resolved returns s, or TargetSelectionFull for the zero value. No
// value of this type is a permissive "run nothing" zero (06 §5.20): an
// unset TargetSelection means "this was never actually decided", and the
// only safe reading of that is "run everything".
func (s TargetSelection) Resolved() TargetSelection {
	if s == "" {
		return TargetSelectionFull
	}
	return s
}

// AffectedStatus is R-21.173's closed three-member classification of a
// freshly-computed affected set: ok (usable), uncomputable (the stack
// could not produce one at all), or stale (computed but not verifiably
// against candidateTreeHash).
type AffectedStatus string

// The closed three-member AffectedStatus vocabulary.
const (
	AffectedStatusOK           AffectedStatus = "ok"
	AffectedStatusUncomputable AffectedStatus = "uncomputable"
	AffectedStatusStale        AffectedStatus = "stale"
)

// Resolved returns s, or AffectedStatusUncomputable for the zero value --
// the same fail-closed reasoning as TargetSelection.Resolved: an unset
// status must never be read as "ok".
func (s AffectedStatus) Resolved() AffectedStatus {
	if s == "" {
		return AffectedStatusUncomputable
	}
	return s
}

// riskRequiresFull reports whether riskClass is High or Critical -- the
// two AC/S-59.T4 values that force Selection=full regardless of
// AffectedStatus. Compared as plain strings against RiskClassHigh's/
// RiskClassCritical's own literal serialization ("high"/"critical") so
// this package never imports internal/jobs for its RiskClass type.
func riskRequiresFull(riskClass string) bool {
	return riskClass == "high" || riskClass == "critical"
}

// SelectTargets is the ONE selection decision in the tree (R-21.173): a
// package-level func, not a RequirementModel method (sport_updates names
// it "internal/ci/SelectTargets (new func)", distinct from
// "RequirementModel.Affected (new method)" -- m is an explicit parameter
// rather than a receiver so the two stay textually distinguishable at
// every call site). It calls m.Affected, classifies the result as
// ok/uncomputable, then binds an ok result to candidateTreeHash via the
// worktree's own current content-addressed tree identity: Selection=full
// (with []Target{TargetAll}) when the affected set is uncomputable, stale
// relative to candidateTreeHash, or riskClass is High/Critical;
// Selection=affected (with the real target set) only for Low/Normal with
// a fresh set.
func SelectTargets(ctx context.Context, m RequirementModel, changed []string, candidateTreeHash, riskClass string) (CIRequirementPlan, error) {
	targets, err := m.Affected(ctx, changed)
	if err != nil {
		// Confirm round 2 finding (item 3): an m.Affected error is
		// propagated VERBATIM here, not folded into Selection=full, and
		// this is a deliberate, documented exception to R-21.173's
		// "never ship without a fail-closed target set" rule -- not an
		// oversight. Every remaining error m.Affected can return (a
		// missing worktreeRoot, ErrBaseCommitUnknown surfacing through a
		// caller that threads it in, a real affected_cmd exiting
		// non-zero -- ErrAffectedCmdFailed) is a CONFIGURATION or
		// CALLER-INPUT defect, not a legitimate "the affected set cannot
		// be computed right now" condition the way an unrelated broken
		// package elsewhere in the tree is (affected_go.go's own D1 fix
		// folds THAT case into TargetAll, never an error, precisely
		// because it is not a configuration defect). Silently absorbing
		// a broken affected_cmd script into "just run everything" would
		// mask that misconfiguration from whoever owns fixing it
		// indefinitely -- every CI run would quietly do the safe-but-
		// expensive thing forever, with the loud, actionable failure
		// (ErrAffectedCmdFailed) never surfacing anywhere. Callers that
		// want strict fail-closed-on-any-error behavior can already get
		// it themselves: `if err != nil { treat as full }` at the call
		// site is a one-line wrap: this package must not make that
		// choice FOR every caller by swallowing the error here.
		return CIRequirementPlan{}, err
	}
	status := AffectedStatusOK
	switch {
	case isTargetAllFallback(targets):
		status = AffectedStatusUncomputable
	case len(targets) == 0 && len(changed) > 0:
		// Independent fail-closed guard (D1/S-65.T1 CR round, REWORK
		// fix): Affected returning an empty, non-TargetAll set while
		// changed is non-empty is itself a fail-closed condition here,
		// regardless of WHICH bug or misconfiguration in whichever
		// Affected implementation produced it (a mapping bug in the Go
		// path, or a real affected_cmd that legitimately exits zero
		// with no stdout). SelectTargets must never ship
		// Selection=affected with zero Targets.
		status = AffectedStatusUncomputable
	default:
		fresh, hashErr := isFreshAgainst(ctx, m.WorktreeRoot, candidateTreeHash)
		if hashErr != nil {
			return CIRequirementPlan{}, hashErr
		}
		if !fresh {
			status = AffectedStatusStale
		}
	}

	selection := TargetSelectionAffected
	resolvedTargets := targets
	if status.Resolved() != AffectedStatusOK || riskRequiresFull(riskClass) {
		selection = TargetSelectionFull
		resolvedTargets = []Target{TargetAll}
	}
	return CIRequirementPlan{
		Targets:           resolvedTargets,
		Selection:         selection,
		CandidateTreeHash: candidateTreeHash,
		RiskClass:         riskClass,
	}, nil
}

// isTargetAllFallback reports whether targets is exactly the
// affected.go/affected_cmd.go "could not compute a concrete set"
// fallback shape.
func isTargetAllFallback(targets []Target) bool {
	return len(targets) == 1 && targets[0] == TargetAll
}

// isFreshAgainst reports whether worktreeRoot's current content-addressed
// tree identity (`git rev-parse HEAD^{tree}`, a real subprocess) equals
// candidateTreeHash. A blank candidateTreeHash is never fresh: there is
// nothing to have verified the affected set against.
func isFreshAgainst(ctx context.Context, worktreeRoot, candidateTreeHash string) (bool, error) {
	if candidateTreeHash == "" {
		return false, nil
	}
	actual, err := currentTreeHash(ctx, worktreeRoot)
	if err != nil {
		return false, err
	}
	return actual == candidateTreeHash, nil
}

// currentTreeHash runs `git rev-parse HEAD^{tree}` inside worktreeRoot:
// the content-addressed hash of the currently checked-out tree, distinct
// from the commit hash (which also hashes author/committer/message
// metadata) -- matching R-21.147's "content-addressed snapshot" wording.
func currentTreeHash(ctx context.Context, worktreeRoot string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD^{tree}")
	cmd.Dir = worktreeRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", cascade.Wrapf(cascade.KindUnavailable, err,
			"ci: resolving the current tree hash in %q: %s", worktreeRoot, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}
