// Purpose: FILTER 1 (capability match) and FILTER 2 (sensitivity gate),
//   the two security-adjacent filters in Router.Select's five-filter
//   pipeline (§5.16 + §5.22).
// Inputs: the exclude-filtered candidate slice router.go's Select builds
//   from the single routeSnapshot, plus the ModelRequest.
// Outputs: the surviving candidate slice, the accumulated ReasonFlags, or
//   a typed fail-closed error.
// Constraints: fail-closed only - an empty capability set, an unprobed
//   lane, or an emptied sensitivity gate never falls through to an
//   arbitrary lane. See the journal for the CONTRACT DEVIATION on
//   R-21.113's locality predicate: ProviderInfo carries no credential_ref
//   or domain_kind field, so locality here is computed from BaseURL's
//   host only (loopback literal or a host-less unix socket), which is a
//   strict subset of the full predicate the ticket text describes.
// SPORT: conductor.router/ADD (P1-E11-W3-S22-T2).

package conductor

import (
	"fmt"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/provider"
)

// filterCapability is FILTER 1: retain lanes whose provider Capabilities
// satisfies req.RequiredCapabilities. A lane whose relevant capability
// dimension is CapabilityUnknown (never probed) counts as an EMPTY
// capability set and is excluded whenever any capability is required
// (R-21.213) - never treated as "unknown, so allow".
func filterCapability(cands []laneCandidate, req provider.ModelRequest, flags []string) ([]laneCandidate, []string, error) {
	required := req.RequiredCapabilities
	out := make([]laneCandidate, 0, len(cands))
	unprobed := 0
	for _, c := range cands {
		if !anyCapabilityRequired(required) {
			out = append(out, c)
			continue
		}
		if hasUnprobedDimension(c.provider.Capabilities, required) {
			unprobed++
			continue
		}
		if c.provider.Capabilities.Satisfies(required) {
			out = append(out, c)
		}
	}
	if unprobed > 0 {
		flags = append(flags, fmt.Sprintf("capability:%d-unprobed-excluded", unprobed))
	}
	flags = append(flags, fmt.Sprintf("capability:%d-matched", len(out)))
	if len(out) == 0 {
		return out, flags, ErrNoCapableProvider
	}
	return out, flags, nil
}

// anyCapabilityRequired reports whether req sets any capability
// dimension.
func anyCapabilityRequired(req provider.RequiredCapabilities) bool {
	return req.Search || req.URLFetch || req.Vision || req.ToolUse || req.LongContext || req.StructuredOutput
}

// hasUnprobedDimension reports whether any capability dimension req
// requires is still CapabilityUnknown on caps - never probed, and so
// never admitted on the strength of a required dimension.
func hasUnprobedDimension(caps provider.Capabilities, req provider.RequiredCapabilities) bool {
	checks := []struct {
		required bool
		state    provider.CapabilityState
	}{
		{req.Search, caps.Search},
		{req.URLFetch, caps.URLFetch},
		{req.Vision, caps.Vision},
		{req.ToolUse, caps.ToolUse},
		{req.LongContext, caps.LongContext},
		{req.StructuredOutput, caps.StructuredOutput},
	}
	for _, chk := range checks {
		if chk.required && chk.state == provider.CapabilityUnknown {
			return true
		}
	}
	return false
}

// filterSensitivity is FILTER 2 (§5.16 + §5.22, amended by R-21.228 and
// P1-SEC-02): every candidate is decided by the one lane table
// (SensitivityAllows, lane_table.go) at req.Sensitivity, an invalid tier
// reading as restricted. It never widens the tier the envelope resolved.
//
// local-only keeps its R-21.228 contract: the flag is the count alone and an
// emptied set is cascade.ErrNoLane. Every other tier adds one
// "sensitivity:<tier>:removed:<lane>(<lanetype>)" flag per removed lane, and
// an emptied set returns ErrSensitivityViolation.
func filterSensitivity(cands []laneCandidate, req provider.ModelRequest, permits RestrictedPermits, flags []string) ([]laneCandidate, []string, error) {
	tier := effectiveTier(req.Sensitivity)
	v := applyLaneTable(cands, tier, permits)
	flags = append(flags, fmt.Sprintf("sensitivity:%s:%d-filtered", tier.String(), len(v.removed)))
	if tier != provider.SensitivityLocalOnly {
		flags = append(flags, v.flags...)
	}
	if len(v.kept) == 0 && len(cands) > 0 {
		if tier == provider.SensitivityLocalOnly {
			return v.kept, flags, ErrNoLane
		}
		return v.kept, flags, emptiedRefusal(tier, v)
	}
	return v.kept, flags, nil
}

// computedLocalityIsLocal implements the reachable subset of the R-21.113
// predicate given the real ProviderInfo shape: local iff BaseURL names a unix
// socket on this machine (a unix URL with an EMPTY host and an absolute
// path) or its canonical host is a loopback literal. ProviderInfo has no
// credential_ref or domain_kind field to check the remaining two conjuncts
// against (see this file's header comment); this is a documented narrowing,
// never an authored "is local" column and never derived from the provider
// name. The rules (canonical spellings only, matching Go's HTTP proxy
// selection) live in topology.URLIsLocal, the one classifier the fleet
// topology's EndpointIsLocal also uses, so the router table and the lane
// ceiling cannot disagree.
func computedLocalityIsLocal(p provider.ProviderInfo) bool {
	return topology.URLIsLocal(p.BaseURL)
}
