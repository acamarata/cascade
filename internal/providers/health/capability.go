// Purpose: the R-14.88 capability-denied path's cache-marking half:
//   CapabilityKind (the six R-14.88 vocabulary members) and
//   MarkCapabilityUnsupported, which marks one provider's capability
//   unsupported without touching demotion_count or health_status.
//
// CONTRACT DEVIATION (recorded). The ticket's capability_denied task says
// "mark the (lane, capability) pair unsupported in the S-20.T2 registry
// capability cache", but the S-20.T2 CONTRACT this ticket must build on
// (already forged, frozen) gives LaneRecord no capability field at all --
// registry.ProviderRecord.Capabilities is provider-scoped, not lane-scoped
// (schema.go). Marking a lane-scoped pair is not representable against the
// frozen schema. This method marks the capability unsupported at the
// PROVIDER level instead, the closest fulfillable reading, and the
// contradiction is flagged here rather than silently narrowed to
// provider-only in a comment nobody would read.
//
// Inputs: a provider name and a CapabilityKind.
// Outputs: the registry write, via AtomicHealthUpdate (no separate
//   transaction path -- capability marking reuses the same atomic
//   read-modify-write DemoteProvider does, so it is equally race-safe).
// Constraints: never increments demotion_count, never changes
//   health_status, never advances pool rotation (it only ever touches
//   Capabilities) -- exactly the golden test's "pool untouched after N
//   capability-denied responses" assertion.
// SPORT: provider.health/ADD (P1-E10-W3-S20-T3).

package health

import (
	"context"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// CapabilityKind names one of the six R-14.88 tool-capability vocabulary
// members a probe or a live request can find unsupported.
type CapabilityKind string

// The six closed CapabilityKind members, matching pkg/provider.
// Capabilities' field set exactly.
const (
	CapabilitySearch           CapabilityKind = "search"
	CapabilityURLFetch         CapabilityKind = "url_fetch"
	CapabilityVision           CapabilityKind = "vision"
	CapabilityToolUse          CapabilityKind = "tool_use"
	CapabilityLongContext      CapabilityKind = "long_context"
	CapabilityStructuredOutput CapabilityKind = "structured_output"
)

// Valid reports whether k is one of the six declared members.
func (k CapabilityKind) Valid() bool {
	switch k {
	case CapabilitySearch, CapabilityURLFetch, CapabilityVision, CapabilityToolUse, CapabilityLongContext, CapabilityStructuredOutput:
		return true
	}
	return false
}

// MarkCapabilityUnsupported marks capability unsupported on name's cached
// Capabilities set, without touching demotion_count, health_status, or
// pool rotation. Not part of pkg/provider.HealthManager (that interface's
// DemoteProvider has no capability parameter -- see health.go's doc
// comment); callers that classify a capability-mismatch response call
// this directly against a concrete *Manager.
func (m *Manager) MarkCapabilityUnsupported(ctx context.Context, name string, capability CapabilityKind) error {
	if !capability.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "health: invalid capability %q", capability)
	}
	_, err := m.reg.AtomicHealthUpdate(ctx, name, func(rec registry.ProviderRecord) (registry.ProviderRecord, error) {
		setCapabilityUnsupported(&rec.Capabilities, capability)
		return rec, nil
	})
	return err
}

// setCapabilityUnsupported flips the named field on caps to
// CapabilityUnsupported, leaving every other field untouched.
func setCapabilityUnsupported(caps *provider.Capabilities, capability CapabilityKind) {
	switch capability {
	case CapabilitySearch:
		caps.Search = provider.CapabilityUnsupported
	case CapabilityURLFetch:
		caps.URLFetch = provider.CapabilityUnsupported
	case CapabilityVision:
		caps.Vision = provider.CapabilityUnsupported
	case CapabilityToolUse:
		caps.ToolUse = provider.CapabilityUnsupported
	case CapabilityLongContext:
		caps.LongContext = provider.CapabilityUnsupported
	case CapabilityStructuredOutput:
		caps.StructuredOutput = provider.CapabilityUnsupported
	}
}
