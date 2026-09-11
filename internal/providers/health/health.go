// Package health implements the provider registry's health/eviction
// subsystem (P1-E10-W3-S20-T3): Manager, the HealthManagerImpl -- the
// health state machine (DemoteProvider), the read-only surface
// (GetHealth), and health-changed event emission. probe.go owns
// RecoverProbe (the egress-gated recovery leg); scheduler.go owns the
// background probe goroutine; capability.go owns the R-14.88
// capability-cache marking.
//
// STATE MACHINE (R-14.33, ratified): DemoteProvider is the single writer
//
//	of health_status/health_checked_at/demotion_count -- every transition
//	in the ticket's HEALTH STATE MACHINE table routes through it or
//	RecoverProbe (probe.go). reason=dead_key_hard bypasses the threshold
//	and writes dead immediately, regardless of demotion_count. Every other
//	reason except capability_denied increments demotion_count and sets
//	degraded, or dead once demotion_count reaches the configured
//	threshold. reason=capability_denied is a documented no-op here (see
//	the interface's own doc comment for the signature contradiction this
//	resolves).
//
// Inputs: an internal/providers/registry.Registry, an internal/events.Bus,
//
//	an internal/runtime.Clock, and an eviction threshold (0 defaults to
//	DefaultEvictionThreshold=3 per R-14.33).
//
// Outputs: registry state changes and, on every observed health_status
//
//	flip, exactly one EventKindHealthChanged event.
//
// Constraints: no bare time.Now (every timestamp is m.clock.Now()); every
//
//	write goes through registry.AtomicHealthUpdate, so concurrent callers
//	on the same provider name never lose an update (TestConcurrentDemote).
//
// SPORT: provider.health/ADD (P1-E10-W3-S20-T3).
package health

import (
	"context"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// eventNamespace is the internal/events.Bus namespace every health event
// publishes to.
const eventNamespace = "provider-health"

// eventSource identifies this package as the events.Event.Source on every
// publish.
const eventSource = "provider-health"

// EventKindHealthChanged is minted by this package (internal/events'
// EventKind vocabulary is deliberately open -- see events/types.go's doc
// comment) for every observed health_status transition.
const EventKindHealthChanged events.EventKind = "provider.health.changed"

// DefaultEvictionThreshold is R-14.33's ratified default: three
// consecutive threshold-path demotions dead-evict a provider.
const DefaultEvictionThreshold = 3

// ChangedPayload is EventKindHealthChanged's JSON payload shape. Endpoint/
// ProbeStatus are populated only when the transition was observed via a
// RecoverProbe micro-verify (empty/zero for a DemoteProvider-driven
// transition, which carries no probed endpoint) -- see probe.go's
// LastProbeResult for the "probe failure detail" acceptance criterion's
// storage half. Neither field is ever a credential: ProbeResult.Endpoint
// is the production Prober's redacted URL, never a raw key (probe.go's
// own doc comment).
type ChangedPayload struct {
	ProviderName  string    `json:"provider_name"`
	PrevStatus    string    `json:"prev_status"`
	NewStatus     string    `json:"new_status"`
	Reason        string    `json:"reason"`
	DemotionCount int       `json:"demotion_count"`
	Timestamp     time.Time `json:"timestamp"`
	Endpoint      string    `json:"endpoint,omitempty"`
	ProbeStatus   int       `json:"probe_status,omitempty"`
}

// Manager is the write-capable health manager. The zero value is not
// usable; construct with NewManager.
type Manager struct {
	reg       *registry.Registry
	bus       *events.Bus
	clock     runtime.Clock
	threshold int
	egress    Egress
	prober    Prober
	backoff   *backoffState
	lastProbe *lastProbeResults
}

// NewManager returns a ready-to-use Manager. evictionThreshold<=0 uses
// DefaultEvictionThreshold. RecoverProbe refuses (fail closed) until
// WithEgress configures an Egress gate and a Prober.
func NewManager(reg *registry.Registry, bus *events.Bus, clock runtime.Clock, evictionThreshold int) *Manager {
	if evictionThreshold <= 0 {
		evictionThreshold = DefaultEvictionThreshold
	}
	return &Manager{reg: reg, bus: bus, clock: clock, threshold: evictionThreshold, backoff: newBackoffState(), lastProbe: newLastProbeResults()}
}

// compile-time assertion: Manager implements pkg/provider.HealthManager.
var _ provider.HealthManager = (*Manager)(nil)

// DemoteProvider implements provider.HealthManager. See the interface's
// own doc comment for the full transition table and the
// capability_denied no-op.
func (m *Manager) DemoteProvider(ctx context.Context, name string, reason provider.DemotionReason) error {
	if name == "" {
		return cascade.New(cascade.KindInvalidInput, "health: provider name is required")
	}
	if !reason.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "health: invalid demotion reason %q", reason)
	}
	if reason == provider.ReasonCapabilityDenied {
		// R-14.88: no demotion_count increment, no health_status change,
		// no pool-rotation advance. Confirm the provider exists (a
		// caller demoting an unknown name should still see
		// ErrProviderNotFound) and stop -- no write, no event.
		_, err := m.reg.GetProvider(ctx, name)
		return err
	}

	var prevStatus registry.HealthStatus
	updated, err := m.reg.AtomicHealthUpdate(ctx, name, func(rec registry.ProviderRecord) (registry.ProviderRecord, error) {
		prevStatus = rec.HealthStatus
		now := m.clock.Now()
		if reason == provider.ReasonDeadKeyHard {
			// Hard dead-key path: immediate, threshold-bypassing.
			// demotion_count is left untouched -- it is the
			// threshold-path counter, and the hard path never consults
			// it (R-14.33).
			rec.HealthStatus = registry.HealthDead
			rec.HealthCheckedAt = now
			return rec, nil
		}
		// Threshold path: rate_limited_429, dead_key_soft, intake_fail.
		rec.DemotionCount++
		if rec.DemotionCount >= m.threshold {
			rec.HealthStatus = registry.HealthDead
		} else {
			rec.HealthStatus = registry.HealthDegraded
		}
		rec.HealthCheckedAt = now
		return rec, nil
	})
	if err != nil {
		return err
	}
	return m.emitHealthChanged(ctx, name, prevStatus, updated.HealthStatus, string(reason), updated.DemotionCount, nil)
}

// GetHealth implements provider.HealthManager. Read-only; reads directly
// from the registry, no state change.
func (m *Manager) GetHealth(ctx context.Context, name string) (provider.HealthStatus, error) {
	rec, err := m.reg.GetProvider(ctx, name)
	if err != nil {
		return provider.HealthUnknown, err
	}
	return provider.HealthStatus(rec.HealthStatus), nil
}

// emitHealthChanged publishes exactly one EventKindHealthChanged event
// when prev != next (a genuine health_status flip), and publishes nothing
// for a demotion_count-only bump that leaves health_status unchanged --
// matching the acceptance criterion's "every health_status state change
// emits exactly one event" reading literally. probe is the RecoverProbe
// micro-verify that drove this transition, or nil for a DemoteProvider-
// driven transition (no probed endpoint to attach).
func (m *Manager) emitHealthChanged(ctx context.Context, name string, prev, next registry.HealthStatus, reason string, count int, probe *ProbeResult) error {
	if prev == next {
		return nil
	}
	pl := ChangedPayload{
		ProviderName: name, PrevStatus: string(prev), NewStatus: string(next),
		Reason: reason, DemotionCount: count, Timestamp: m.clock.Now(),
	}
	if probe != nil {
		pl.Endpoint, pl.ProbeStatus = probe.Endpoint, probe.Status
	}
	payload, err := json.Marshal(pl)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "health: encode event payload")
	}
	if _, err := m.bus.Publish(ctx, eventNamespace, EventKindHealthChanged, eventSource, payload); err != nil {
		return err
	}
	return nil
}
