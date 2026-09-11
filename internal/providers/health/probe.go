// Purpose: RecoverProbe (P1-E10-W3-S20-T3) -- the egress-gated recovery
//   leg. Acquires the provider-intake egress capability J/S-20.T1 already
//   registers and reuses (R-21.265, never re-registered), then runs a
//   1-token micro-verify through an injected Prober; on success resets
//   demotion_count and health_status=healthy; on a reachable-but-failing
//   probe it advances an in-memory recovery backoff counter, never
//   demotion_count.
//
// CONTRACT DEVIATION (recorded). internal/providers/intake owns the real
// per-driver micro-verify request shaping (transport.go's
// microVerifyBody/microVerifyRequest), but those are unexported and
// intake/*.go is outside this ticket's files_scope (files_scope.change is
// registry.go only), so this package cannot call them directly. Prober is
// this package's own seam: production wiring (constructing a live,
// HTTP-backed Prober) is composition-root work, exactly the gap
// registry/migration.go's own CONTRACT DEVIATION note already names for
// this same reason -- see the ticket journal.
//
// BACKOFF STATE (recorded). The frozen S-20.T2 schema has no
// recovery-backoff column -- ProviderRecord carries no such field, and
// this ticket's own text says the probe goroutine is "advisory": backoff
// state is therefore process-local, held in Manager.backoff (mu-guarded),
// never persisted. A daemon restart resets every provider's backoff to
// its first step, which is the correct, safe default (never worse than an
// unnecessary early retry).
//
// Inputs: a Prober (the production caller's real driver-backed
//   micro-verify implementation) and an *egress.Engine.
// Outputs: (recovered bool, err error); registry state changes on
//   success.
// Constraints: with the provider-intake class unregistered or disabled,
//   RecoverProbe dials nothing and mutates no health state -- asserted by
//   TestRecoverProbeRefusesWhenEgressClassMissing/Disabled (probe_test.go)
//   counting zero Prober.Probe calls.
// SPORT: provider.health/ADD (P1-E10-W3-S20-T3).

package health

import (
	"context"
	"sync"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/cascade"
)

// lastProbeResults is the in-memory, mu-guarded "last observed ProbeResult
// per provider" set -- the storage half of the "probe failure detail"
// acceptance criterion (a future `cascade provider test` command renders
// it; that command is out of this ticket's files_scope, so this is the
// retrievable surface it will read from). Process-local, like
// backoffState: a daemon restart clears it, which is safe -- the next
// scheduled probe repopulates it.
type lastProbeResults struct {
	mu      sync.Mutex
	results map[string]ProbeResult
}

func newLastProbeResults() *lastProbeResults {
	return &lastProbeResults{results: make(map[string]ProbeResult)}
}

func (l *lastProbeResults) set(name string, r ProbeResult) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.results[name] = r
}

func (l *lastProbeResults) get(name string) (ProbeResult, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.results[name]
	return r, ok
}

// ProbeResult is one micro-verify outcome. Endpoint and Status/TransportErr
// are surfaced on the health surface and by `cascade provider test`
// (acceptance criterion "Probe failure detail") -- Endpoint never carries a
// credential (the production Prober is responsible for that, exactly as
// intake/transport.go's probeAttempt.endpoint redacts the Gemini query
// key).
type ProbeResult struct {
	Endpoint     string
	Status       int
	TransportErr string
	Success      bool
}

// Prober runs one 1-token micro-verify against rec and reports the
// outcome. The production implementation is a driver-backed HTTP call
// (composition-root wiring, see this file's CONTRACT DEVIATION note); test
// implementations are recording fakes with no net import (Art.7.2).
type Prober interface {
	Probe(ctx context.Context, rec registry.ProviderRecord) (ProbeResult, error)
}

// Egress abstracts the subset of *egress.Engine RecoverProbe needs, so
// probe_test.go can drive both the real engine (unit-testable, no network)
// and a minimal fake.
type Egress interface {
	Capability(class egress.EgressClass) (egress.Capability, error)
	Registry() *egress.Registry
}

// WithEgress returns a Manager that gates RecoverProbe's outbound leg
// behind eng's provider-intake class (R-21.265) and probes through
// prober. A Manager built with NewManager alone (no WithEgress call) has
// RecoverProbe always refuse -- fail-closed, never a silent skip of the
// egress check.
func (m *Manager) WithEgress(eng Egress, prober Prober) *Manager {
	m.egress = eng
	m.prober = prober
	return m
}

// errEgressClassRefused wraps whatever the egress engine returned. Never
// echoes rec/name -- a provider name alone carries no credential, but this
// keeps the pattern consistent with intake's own errEgressClassRefused.
func errEgressClassRefused(cause error) error {
	return cascade.Wrap(cascade.KindCapabilityDenied, cause, "health: provider-intake egress class refused")
}

// RecoverProbe implements provider.HealthManager. See probe.go's own
// header doc comment for the full contract.
func (m *Manager) RecoverProbe(ctx context.Context, name string) (bool, error) {
	if name == "" {
		return false, cascade.New(cascade.KindInvalidInput, "health: provider name is required")
	}
	if m.egress == nil || m.prober == nil {
		return false, errEgressClassRefused(cascade.New(cascade.KindCapabilityDenied, "health: RecoverProbe has no egress/prober configured (fail closed)"))
	}
	if _, err := m.egress.Capability(egress.EgressClassProviderIntake); err != nil {
		return false, errEgressClassRefused(err)
	}
	cfg, ok := m.egress.Registry().Lookup(egress.EgressClassProviderIntake)
	if !ok || !cfg.Enabled {
		return false, errEgressClassRefused(cascade.New(cascade.KindCapabilityDenied, "health: provider-intake class disabled"))
	}

	rec, err := m.reg.GetProvider(ctx, name)
	if err != nil {
		return false, err
	}

	result, perr := m.prober.Probe(ctx, rec)
	m.recordLastProbe(name, result)
	if perr != nil || !result.Success {
		// Fail closed: an error return, an unsuccessful probe, or any
		// unparseable/ambiguous outcome all take this branch -- none of
		// them ever set health_status=healthy. There is no "assume
		// healthy" fallthrough.
		m.advanceBackoff(name)
		return false, nil
	}
	m.resetBackoff(name)

	var prevStatus registry.HealthStatus
	updated, err := m.reg.AtomicHealthUpdate(ctx, name, func(cur registry.ProviderRecord) (registry.ProviderRecord, error) {
		prevStatus = cur.HealthStatus
		cur.HealthStatus = registry.HealthHealthy
		cur.DemotionCount = 0
		cur.HealthCheckedAt = m.clock.Now()
		return cur, nil
	})
	if err != nil {
		return false, err
	}
	if err := m.emitHealthChanged(ctx, name, prevStatus, updated.HealthStatus, "recovered", 0, &result); err != nil {
		return false, err
	}
	return true, nil
}

// backoffState is the in-memory, mu-guarded recovery-backoff counter set
// (see this file's BACKOFF STATE note).
type backoffState struct {
	mu    sync.Mutex
	steps map[string]int
}

func newBackoffState() *backoffState { return &backoffState{steps: make(map[string]int)} }

func (b *backoffState) advance(name string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.steps[name]++
	return b.steps[name]
}

func (b *backoffState) reset(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.steps, name)
}

func (b *backoffState) get(name string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.steps[name]
}

func (m *Manager) advanceBackoff(name string) { m.backoff.advance(name) }
func (m *Manager) resetBackoff(name string)   { m.backoff.reset(name) }

// BackoffSteps returns name's current recovery-backoff step count (0 if
// never failed, or already recovered). Exported for the scheduler's
// interval calculation (scheduler.go) and for tests.
func (m *Manager) BackoffSteps(name string) int { return m.backoff.get(name) }

// recordLastProbe stores r as name's most recently observed ProbeResult,
// success or failure alike -- the "probe failure detail" acceptance
// criterion's storage half.
func (m *Manager) recordLastProbe(name string, r ProbeResult) { m.lastProbe.set(name, r) }

// LastProbeResult returns name's most recently observed ProbeResult and
// true, or a zero ProbeResult and false if RecoverProbe has never run for
// name (never dialed, or the egress class refused before a Prober call was
// made). Exported for a future `cascade provider test` render and for
// tests; Endpoint/TransportErr are whatever the configured Prober
// returned, which per this package's own contract never carries a
// credential.
func (m *Manager) LastProbeResult(name string) (ProbeResult, bool) { return m.lastProbe.get(name) }
