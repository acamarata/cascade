// Purpose: HealthManager, the read+demotion interface a pkg/-layer
//   consumer (the K/S-22 conductor) uses to query and demote a provider's
//   health, and the closed HealthStatus/DemotionReason vocabularies it
//   speaks in. The write-capable implementation (state machine, probe
//   scheduler, egress-gated recovery probe) lives in internal/providers/
//   health/ only, matching pkg/provider/registry.go's
//   ProviderRegistryReader precedent: this file declares the seam and its
//   data shapes so a caller never needs internal/.
// Inputs: none -- interface and closed-vocabulary declarations only.
// Outputs: none.
// Constraints: pkg/provider imports nothing from internal/ (Art.10.2); the
//   repo-wide arch test asserts this boundary for every file in pkg/.
//   HealthStatus and DemotionReason mirror internal/providers/registry's
//   own closed vocabularies (schema.go's HealthStatus, and this ticket's
//   DemotionReason) exactly, in value, so no translation table is needed.
// SPORT: pkg.provider.health_manager/ADD (P1-E10-W3-S20-T3).

package provider

import "context"

// HealthStatus is the S-20.T3-managed provider health state. HealthUnknown
// is deliberately the zero value, mirroring internal/providers/registry.
// HealthStatus exactly.
type HealthStatus string

// The four closed HealthStatus members. HealthUnknown is the zero value.
const (
	HealthUnknown  HealthStatus = ""
	HealthHealthy  HealthStatus = "healthy"
	HealthDegraded HealthStatus = "degraded"
	HealthDead     HealthStatus = "dead"
)

// Valid reports whether h is one of the four declared members.
func (h HealthStatus) Valid() bool {
	switch h {
	case HealthUnknown, HealthHealthy, HealthDegraded, HealthDead:
		return true
	}
	return false
}

// DemotionReason is the closed vocabulary a DemoteProvider caller states,
// ratified by R-14.33/R-14.88.
type DemotionReason string

// The five closed DemotionReason members.
const (
	// ReasonRateLimited429 is a 429 response; advances demotion_count
	// under the threshold path.
	ReasonRateLimited429 DemotionReason = "rate_limited_429"
	// ReasonDeadKeyHard is a 401/403 with no Retry-After header: an
	// immediate, threshold-bypassing dead-key signal.
	ReasonDeadKeyHard DemotionReason = "dead_key_hard"
	// ReasonDeadKeySoft is an auth error carrying a Retry-After header
	// (or otherwise ambiguous): advances demotion_count under the
	// threshold path rather than an immediate hard-dead write.
	ReasonDeadKeySoft DemotionReason = "dead_key_soft"
	// ReasonIntakeFail is a non-HTTP transport failure observed during
	// intake or a probe: advances demotion_count under the threshold
	// path.
	ReasonIntakeFail DemotionReason = "intake_fail"
	// ReasonCapabilityDenied (R-14.88) is a 429/400 on a request carrying
	// a capability the provider does not support. It MUST NOT advance
	// demotion_count and MUST NOT change health_status -- see
	// DemoteProvider's doc comment.
	ReasonCapabilityDenied DemotionReason = "capability_denied"
)

// Valid reports whether r is one of the five declared members.
func (r DemotionReason) Valid() bool {
	switch r {
	case ReasonRateLimited429, ReasonDeadKeyHard, ReasonDeadKeySoft, ReasonIntakeFail, ReasonCapabilityDenied:
		return true
	}
	return false
}

// HealthManager is the read+demotion surface a router (K/S-22 conductor)
// and intake (S-20.T1) consume. The write-capable implementation is
// internal/providers/health.Manager; it is unreachable from pkg/ or cmd/
// by construction -- this interface never names it.
//
//nolint:revive // contract-mandated name (04-PEWS-PLAN-W1-W3.md §Epic J S-20.T3: "pkg/provider.HealthManager"); the stutter with package provider is deliberate.
type HealthManager interface {
	// DemoteProvider records one adverse signal for name under reason.
	// reason=dead_key_hard sets health_status=dead immediately,
	// bypassing demotion_count. Every other reason except
	// capability_denied increments demotion_count and sets health_status
	// to degraded, or dead once demotion_count reaches the configured
	// eviction threshold. reason=capability_denied is a documented no-op
	// on demotion_count and health_status (R-14.88) -- callers that need
	// to record a capability-mismatch signal use the capability-cache
	// entry point their concrete HealthManager implementation exposes
	// beyond this interface (internal/providers/health.Manager.
	// MarkCapabilityUnsupported), not DemoteProvider: this interface's
	// signature has no capability parameter to carry which capability was
	// denied, so DemoteProvider alone cannot fulfil the cache-marking half
	// of the capability_denied contract -- see the ticket journal for the
	// full contradiction.
	DemoteProvider(ctx context.Context, name string, reason DemotionReason) error
	// RecoverProbe runs a 1-token micro-verify against name. On success
	// it resets demotion_count to 0, sets health_status=healthy, and
	// returns (true, nil). On a reachable-but-failing probe it advances
	// an internal recovery backoff counter (never demotion_count) and
	// returns (false, nil). RecoverProbe returns a typed
	// cascade.KindCapabilityDenied error, leaving health state
	// unchanged, when the provider-intake egress class is unregistered
	// or disabled (R-21.265).
	RecoverProbe(ctx context.Context, name string) (recovered bool, err error)
	// GetHealth returns name's current health_status. Read-only; never
	// mutates registry state.
	GetHealth(ctx context.Context, name string) (HealthStatus, error)
}
