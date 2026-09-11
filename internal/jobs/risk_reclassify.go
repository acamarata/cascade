package jobs

// Purpose: Reclassify (R-21.146), the checkpoint entry point that
//
//	re-runs the step-4 classifier over the ACTUAL changed-path set
//	instead of the plan-time predicted footprint, at every lease
//	checkpoint and once more before acceptance.
//
// Inputs: the planned RiskClass, a ChangeFootprint (the candidate
//
//	tree's actual changed paths plus the plan input's resolved
//	dependency-impact paths), the holding lease's normalized scope
//	prefixes (R-21.168), and the classifier's content-probe root.
//
// Outputs: the (possibly unchanged) RiskClass, a non-nil *Escalation
//
//	only when the class increased, and the changed paths not covered by
//	the lease's scope prefixes (independent of escalation).
//
// Constraints: MONOTONIC -- within one attempt the class only
//
//	increases; a derived class at or below planned returns planned
//	UNCHANGED, never a downgrade. The gate that denies/requeues on
//	escalation or containment violation is AC/S-60.T3's; the checkpoint
//	call site is AF/S-65.T2's -- neither is implemented here.
//
// SPORT: jobs/risk-classifier/ADD (P1-E29-W6-S59-T4).

import (
	"context"
	"strings"
)

// ChangeFootprint is the ACTUAL footprint Reclassify re-classifies,
// as distinct from a TicketInput's plan-time DECLARED footprint:
// ChangedPaths is the candidate tree's real diff (AC/S-59.T3's
// materialization); DependencyImpactPaths is the plan input's resolved
// deps' impact, carried separately because a dependency's impact is not
// itself part of "what this attempt changed" for scope-containment
// purposes (see outOfScope below, which checks ChangedPaths only).
type ChangeFootprint struct {
	ChangedPaths          []string
	DependencyImpactPaths []string
}

// Escalation is the R-21.146 result of a class increase: evidence
// gathered under InvalidatedGateSet (the planned class's gates) is
// invalidated, RequiredGateSet (the derived class's gates) applies, and
// RequiredScopePrefixes must be held before the attempt continues.
type Escalation struct {
	From                  RiskClass
	To                    RiskClass
	TriggeringPaths       []string
	InvalidatedGateSet    GateSet
	RequiredGateSet       GateSet
	RequiredScopePrefixes []string
}

// Reclassify re-runs classifyFootprint over actual's union instead of
// the plan-time footprint. probeRoot is threaded through for the same
// content-probe reasons Planner.classify needs it; this function takes
// no ReachabilityFn -- the actual footprint is a real diff, not a
// declared plan-time footprint, so there is no pre/post-image
// distinction left for a reachability seam to widen (see this ticket's
// journal: a documented signature narrowing from the ticket's own
// four-parameter prose, needed for the content probes to function).
//
//nolint:revive // ctx is the ticket-contract-mandated signature (HOW-7: "Reclassify(ctx, planned, actual, leaseScope)"); kept for cancellation/future use even though this pure classification pass does not read it yet.
func Reclassify(ctx context.Context, planned RiskClass, actual ChangeFootprint, leaseScopePrefixes []string, probeRoot string) (derived RiskClass, esc *Escalation, outOfScope []string, err error) {
	union := distinct(append(append([]string{}, actual.ChangedPaths...), actual.DependencyImpactPaths...))
	derived = classifyFootprint(union, singleRepository(), probeRoot)
	outOfScope = uncoveredPaths(actual.ChangedPaths, leaseScopePrefixes)

	if riskRank[derived] <= riskRank[planned] {
		return planned, nil, outOfScope, nil
	}

	plannedGates, err := GateSetForRiskClass(planned)
	if err != nil {
		return derived, nil, outOfScope, err
	}
	derivedGates, err := GateSetForRiskClass(derived)
	if err != nil {
		return derived, nil, outOfScope, err
	}

	return derived, &Escalation{
		From:                  planned,
		To:                    derived,
		TriggeringPaths:       triggeringPaths(union, probeRoot),
		InvalidatedGateSet:    plannedGates,
		RequiredGateSet:       derivedGates,
		RequiredScopePrefixes: union,
	}, outOfScope, nil
}

// uncoveredPaths returns the changed paths not covered by any of
// prefixes. An empty prefixes set covers nothing, so every changed
// path is reported -- a lease holding zero scope authorizes zero
// paths.
func uncoveredPaths(changed []string, prefixes []string) []string {
	var out []string
	for _, p := range changed {
		covered := false
		for _, prefix := range prefixes {
			if prefix != "" && strings.HasPrefix(p, prefix) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, p)
		}
	}
	return out
}

// triggeringPaths names, best-effort, which paths in footprint match a
// Critical or High rule -- diagnostic detail for Escalation, not part
// of the classification decision itself (classifyFootprint already
// made that decision over the whole footprint).
func triggeringPaths(footprint []string, probeRoot string) []string {
	var out []string
	for _, p := range footprint {
		if _, ok := criticalFloorMatch(p); ok {
			out = append(out, p)
			continue
		}
		if matchesCriticalPaths([]string{p}) || matchesHighPaths([]string{p}) {
			out = append(out, p)
			continue
		}
		if strings.HasPrefix(p, "pkg/") {
			out = append(out, p)
			continue
		}
		if hasContentMarker([]string{p}, probeRoot, "sync/atomic") || hasContentMarker([]string{p}, probeRoot, "go func") {
			out = append(out, p)
			continue
		}
		if hasDropAlterMigration([]string{p}, probeRoot) {
			out = append(out, p)
		}
	}
	return out
}
