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
//   host only (loopback or unix-socket), which is a strict subset of the
//   full predicate the ticket text describes.
// SPORT: conductor.router/ADD (P1-E11-W3-S22-T2).

package conductor

import (
	"fmt"
	"net/url"
	"strings"

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

// filterSensitivity is FILTER 2 (§5.16 + §5.22, amended by R-21.228):
// local-only retains exactly the lanes whose computed locality is local
// and removes every remote-locality lane; restricted removes lanes not
// reachable at worker-trusted or better. It never widens the tier the
// envelope resolved.
func filterSensitivity(cands []laneCandidate, req provider.ModelRequest, flags []string) ([]laneCandidate, []string, error) {
	tier := req.Sensitivity
	if !tier.Valid() {
		tier = provider.SensitivityRestricted
	}
	switch tier {
	case provider.SensitivityLocalOnly:
		return filterLocalOnly(cands, flags)
	case provider.SensitivityRestricted, provider.SensitivityInternal, provider.SensitivityPublic:
		// CONTRACT DEVIATION: the real ProviderRegistryReader carries no
		// node-trust-tier vocabulary (worker-trusted/paired-device), so
		// the restricted/internal/public legs cannot remove candidates
		// against §5.22's ladder; see the journal. They remove nothing
		// and record the fact.
		flags = append(flags, fmt.Sprintf("sensitivity:%s:0-filtered", tier.String()))
		return cands, flags, nil
	default:
		flags = append(flags, fmt.Sprintf("sensitivity:%s:0-filtered", tier.String()))
		return cands, flags, nil
	}
}

// filterLocalOnly implements the local-only leg of FILTER 2 (R-21.228):
// retain lanes whose computed locality is local, remove the rest.
// cascade.ErrNoLane (not ErrSensitivityViolation) is returned when no
// local-locality lane existed to begin with - that is the concrete
// reason an emptied local-only candidate set always has here, since this
// leg removes only remote-locality lanes.
func filterLocalOnly(cands []laneCandidate, flags []string) ([]laneCandidate, []string, error) {
	out := make([]laneCandidate, 0, len(cands))
	removed := 0
	for _, c := range cands {
		if computedLocalityIsLocal(c.provider) {
			out = append(out, c)
		} else {
			removed++
		}
	}
	flags = append(flags, fmt.Sprintf("sensitivity:local-only:%d-filtered", removed))
	if len(out) == 0 && len(cands) > 0 {
		return out, flags, ErrNoLane
	}
	return out, flags, nil
}

// computedLocalityIsLocal implements the reachable subset of the R-21.113
// predicate given the real ProviderInfo shape: local iff BaseURL's host
// is loopback or a unix socket. ProviderInfo has no credential_ref or
// domain_kind field to check the remaining two conjuncts against (see
// this file's header comment); this is a documented narrowing, never an
// authored "is local" column and never derived from the provider name.
func computedLocalityIsLocal(p provider.ProviderInfo) bool {
	if p.BaseURL == "" {
		return false
	}
	if strings.HasPrefix(p.BaseURL, "unix://") {
		return true
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || host == "::1" {
		return true
	}
	// IPv4 loopback range is 127.0.0.0/8; checking the literal dotted
	// prefix avoids importing "net" (an egress-firewall-gated package,
	// A-T2) for a computation that never opens a socket.
	return strings.HasPrefix(host, "127.")
}
