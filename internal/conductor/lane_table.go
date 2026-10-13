// Purpose: the ONE (sensitivity tier x lane type) table. The router's
//   sensitivity filter (FILTER 2) and the thread-privacy filter (FILTER 0)
//   both decide every lane through SensitivityAllows, so the two gates can
//   never reach opposite verdicts about the same lane (C5: one table).
// Inputs: a sensitivity tier, a lane's computed type, and whether the
//   operator named that lane in [conductor] restricted_external_lanes.
// Outputs: an allow/refuse verdict per lane; the partition helpers that turn
//   verdicts into a kept slice, sorted "removed" explain flags and the
//   emptied-set refusal.
// Constraints: FAIL CLOSED. A tier outside the four reads as restricted. An
//   unresolved lane is refused below public. The permit only ever widens the
//   restricted x external-api cell; it never admits an unresolved lane and
//   never changes any other cell.
// SPORT: conductor.lane-table/ADD (P1-SEC-02).

package conductor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// RestrictedPermitsKey is the guarded config key that names the external
// lanes restricted content may use. It is quoted in every refusal that the
// permit list could have prevented, so an operator reading the error knows
// which knob exists.
const RestrictedPermitsKey = "conductor.restricted_external_lanes"

// effectiveTier applies §5.16's fail-closed rule: a tier outside the four
// named members is restricted.
func effectiveTier(t provider.SensitivityTier) provider.SensitivityTier {
	if !t.Valid() {
		return provider.SensitivityRestricted
	}
	return t
}

// SensitivityAllows is the single table. Every cell is decided here and
// lane_table_test.go asserts all twelve:
//
//	tier \ lane | controller-local | external-api            | unresolved
//	local-only  | allow            | refuse                  | refuse
//	restricted  | allow            | allow only if permitted | refuse
//	internal    | allow            | allow                   | refuse
//	public      | allow            | allow                   | allow
//
// restrictedPermitted is consulted for the restricted x external-api cell
// alone. A tier outside the four reads as restricted, and a LaneType nobody
// declared reads as unresolved, so the table is total over both uint8
// ranges.
func SensitivityAllows(t provider.SensitivityTier, lane LaneType, restrictedPermitted bool) bool {
	t = effectiveTier(t)
	switch lane {
	case LaneControllerLocal:
		// Work that never leaves this machine is what every tier permits.
		return true
	case LaneExternalAPI:
		switch t {
		case provider.SensitivityInternal, provider.SensitivityPublic:
			return true
		case provider.SensitivityRestricted:
			return restrictedPermitted
		case provider.SensitivityLocalOnly:
			return false
		default:
			return false
		}
	case LaneUnresolved:
		// Only content already declared public may ride a lane whose
		// destination cannot be named.
		return t == provider.SensitivityPublic
	default:
		// A LaneType nobody declared reads as unresolved.
		return t == provider.SensitivityPublic
	}
}

// removedFlag is the explain flag naming one lane the table removed.
func removedFlag(t provider.SensitivityTier, lane string, lt LaneType) string {
	return fmt.Sprintf("sensitivity:%s:removed:%s(%s)", effectiveTier(t).String(), lane, lt.String())
}

// admitLane is the single-lane form of the table: it classifies c, asks
// SensitivityAllows, and for a refused lane returns the removed flag and a
// KindPolicyDenied error wrapping ErrSensitivityViolation. An admitted lane
// returns "", nil.
func admitLane(c laneCandidate, t provider.SensitivityTier, permits RestrictedPermits) (flag string, err error) {
	t = effectiveTier(t)
	lt := ClassifyLane(c.provider)
	if SensitivityAllows(t, lt, permits.Permits(c.lane.LaneName)) {
		return "", nil
	}
	return removedFlag(t, c.lane.LaneName, lt), cascade.Wrapf(cascade.KindPolicyDenied, ErrSensitivityViolation,
		"conductor: %s content may not use lane %q (%s)", t.String(), c.lane.LaneName, lt.String())
}

// laneVerdict is the outcome of running one tier over a candidate slice.
type laneVerdict struct {
	kept []laneCandidate
	// removed holds the refused candidates sorted by lane name, so flags and
	// the first-removed lane in a refusal are deterministic.
	removed []laneCandidate
	// flags is one removed flag per refused lane, in removed order.
	flags []string
}

// applyLaneTable runs admitLane over every candidate.
func applyLaneTable(cands []laneCandidate, t provider.SensitivityTier, permits RestrictedPermits) laneVerdict {
	v := laneVerdict{kept: make([]laneCandidate, 0, len(cands))}
	flagOf := make(map[string]string)
	for _, c := range cands {
		flag, err := admitLane(c, t, permits)
		if err != nil {
			v.removed = append(v.removed, c)
			flagOf[c.lane.LaneName] = flag
			continue
		}
		v.kept = append(v.kept, c)
	}
	sort.SliceStable(v.removed, func(i, j int) bool { return v.removed[i].lane.LaneName < v.removed[j].lane.LaneName })
	for _, c := range v.removed {
		v.flags = append(v.flags, flagOf[c.lane.LaneName])
	}
	return v
}

// refusalHint tells the operator what could change the verdict for the
// removed lanes at tier t. A permit only widens restricted x external-api; an
// unresolved lane (no host in its BaseURL, which includes a driver-default
// BaseURL) can only be fixed by giving it an explicit one. Every removed lane
// is considered, so a mixed set never hides the permit key.
func refusalHint(t provider.SensitivityTier, removed []laneCandidate) string {
	t = effectiveTier(t)
	var parts []string
	var external, unresolved bool
	for _, c := range removed {
		switch ClassifyLane(c.provider) {
		case LaneExternalAPI:
			external = true
		case LaneUnresolved:
			unresolved = true
		case LaneControllerLocal:
		}
	}
	if external && t == provider.SensitivityRestricted {
		parts = append(parts, "external lanes permitted for restricted content are listed in "+RestrictedPermitsKey+" (default empty)")
	}
	if unresolved {
		fix := "set an explicit base URL on its provider"
		if t == provider.SensitivityLocalOnly {
			fix = "give its provider a loopback or unix-socket base URL"
		}
		parts = append(parts, "a lane has no resolvable host ("+fix+"; a permit never admits it)")
	}
	if len(parts) == 0 {
		return "this tier does not allow that lane type"
	}
	return strings.Join(parts, "; ")
}

// emptiedRefusal is the ErrSensitivityViolation an emptied set returns. It
// names the tier, the first removed lane and its type, the count, and what
// could change the verdict, so the operator sees both what was refused and
// the knob that could change it.
func emptiedRefusal(t provider.SensitivityTier, v laneVerdict) error {
	t = effectiveTier(t)
	first := v.removed[0]
	return &laneRefusal{cascade.Wrapf(cascade.KindPolicyDenied, ErrSensitivityViolation,
		"conductor: %s content may not use lane %q (%s); %d candidate lane(s) removed, none remain; %s",
		t.String(), first.lane.LaneName, ClassifyLane(first.provider).String(), len(v.removed), refusalHint(t, v.removed))}
}
