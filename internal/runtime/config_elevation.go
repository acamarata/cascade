package runtime

// Purpose: the loosening-via-elevation gate. A proposed config change
//
//	that loosens a guarded family used to be refused unconditionally;
//	it now transits the local elevation-approval flow, and is refused
//	when that flow does not approve it.
//
// Inputs: the LooseningPath set hotreload_security.go's CompareSecurity
//
//	computed, and the injected ElevationApprover.
//
// Outputs: the approval reference recorded in the audit trail on success;
//
//	an *ElevationRequiredError naming the loosened keys on refusal.
//
// Constraints: fail closed, in both the absent and the failing case. A
//
//	reloader with no approver enrolled refuses; an approver that errors
//	refuses; an approver that returns no reference refuses. There is no
//	bypass flag and no configuration in which a loosening write lands
//	unapproved. Tightening never reaches this file.
//
// SPORT: runtime/config-loosening-elevation (ADD, P1-E09-W2-S18-T5).

import (
	"context"
	"sort"
	"strings"
)

// elevationGuardedFamilies is the set of config families whose loosening
// requires elevation. It is the SAME list baseline.go guards, read from
// there rather than re-spelled, so the two can never disagree.
func elevationGuardedFamilies() []string { return baselineGuardedSections }

// ElevationApprover is the local elevation-approval seam. The composition
// root wires the concrete implementation to the elevation helper; this
// package depends on the interface only, so internal/runtime keeps its
// no-internal-imports position at the bottom of the dependency graph.
//
// NO production implementation exists yet anywhere in this tree, which is
// stated here rather than left implicit (Art.1).
type ElevationApprover interface {
	// ApproveLoosening asks the local elevation helper to approve
	// loosening the named keys. A nil error authorizes this one write and
	// must be accompanied by a non-empty reference the audit trail
	// records; any error refuses it.
	ApproveLoosening(ctx context.Context, paths []LooseningPath) (string, error)
}

// ElevationRequiredError reports that a config change loosens one or more
// guarded keys and was not approved. It names every loosened key, so the
// operator is told what to approve rather than only that something was
// refused.
type ElevationRequiredError struct {
	// Keys are the loosened keys, sorted.
	Keys []string
	// Reason says why approval did not happen: no helper enrolled, or the
	// helper's own refusal.
	Reason string
}

// Error implements the error interface.
func (e *ElevationRequiredError) Error() string {
	return "runtime: elevation required to loosen " + strings.Join(e.Keys, ", ") + ": " + e.Reason
}

// newElevationRequired builds the refusal for paths.
func newElevationRequired(paths []LooseningPath, reason string) *ElevationRequiredError {
	keys := make([]string, 0, len(paths))
	for _, p := range paths {
		keys = append(keys, p.Key)
	}
	sort.Strings(keys)
	return &ElevationRequiredError{Keys: keys, Reason: reason}
}

// SetElevationApprover installs the approval seam loosening writes transit.
// Until it is called, every loosening write is refused with
// ElevationRequired, which is the same answer the operator got before this
// flow existed and is the correct one for a machine with no helper
// enrolled.
func (hr *HotReloader) SetElevationApprover(approver ElevationApprover) {
	hr.elevation = approver
}

// authorizeLoosening runs the elevation flow for paths and returns the
// approval reference to record. A non-nil error is always an
// *ElevationRequiredError, so every caller can surface the loosened keys.
func (hr *HotReloader) authorizeLoosening(ctx context.Context, paths []LooseningPath) (string, error) {
	if hr.elevation == nil {
		return "", newElevationRequired(paths, "no elevation helper is enrolled on this machine")
	}
	ref, err := hr.elevation.ApproveLoosening(ctx, paths)
	if err != nil {
		return "", newElevationRequired(paths, "the elevation helper refused: "+err.Error())
	}
	if ref == "" {
		return "", newElevationRequired(paths, "the elevation helper returned no approval reference")
	}
	return ref, nil
}

// RequiresElevationToLoosen reports whether a `cascade config set` on the
// dotted key touches a family whose loosening needs elevation. It is the
// write-verb side of the same gate Reload applies, so a caller can tell an
// operator what a write will require BEFORE it is attempted.
//
// It answers on the family alone: whether a specific value is actually a
// loosening is CompareSecurity's question, and this function never
// second-guesses it.
func RequiresElevationToLoosen(dotted string) bool {
	segments, err := SplitDottedPath(dotted)
	if err != nil || len(segments) == 0 {
		// An unparseable key is not exempted. A caller that cannot say
		// which family it is writing to is told it needs elevation rather
		// than being waved through on a parse failure.
		return true
	}
	for _, family := range elevationGuardedFamilies() {
		if segments[0] == family {
			return true
		}
	}
	return false
}
