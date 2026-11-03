// Purpose: the per-provider consecutive-transient counter (P1-GWY-09).
//
//	Every driver call's outcome is fed here by the lane-outcome decorator
//	(lane_outcome.go). A transient outcome (pkg/provider.TransientOf)
//	adds one; a success or any non-transient outcome resets the count to
//	zero. The DegradeAfter-th consecutive transient outcome demotes the
//	provider ONCE through the one health writer
//	(provider.HealthManager.DemoteProvider, reason transient_upstream)
//	and resets the count, so the conductor's health filter evicts the
//	provider and work spills to another.
//
// Inputs: the provider name and the call's returned error.
//
// Outputs: at most one DemoteProvider per DegradeAfter consecutive
//
//	transient outcomes. Nothing is returned: a DemoteProvider failure is
//	logged (provider name and error only) and never changes a call's
//	result.
//
// Constraints: counts attempts, not calls, because each driver attempt is
//
//	one observed outcome; the counter is a mutex map on the Resolver, so
//	concurrent calls are race-free. The demotion runs on a context
//	detached from the call's cancellation. Lane state is untouched (that
//	stays P1-WID-11's recorder).
//
// SPORT: internal/providers/dispatch transient_health/ADD (P1-GWY-09).

package dispatch

import (
	"context"
	"log/slog"
	"sync"

	"github.com/acamarata/cascade/pkg/provider"
)

// DegradeAfter is the count of consecutive transient outcomes on one
// provider that demotes it.
const DegradeAfter = 3

// WithHealth gives the Resolver the health writer the transient counter
// demotes through. A Resolver built without it counts nothing.
func WithHealth(h provider.HealthManager) ResolverOption {
	return func(r *Resolver) { r.health = h }
}

// transientCounter is the per-provider consecutive-transient state.
type transientCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

// observe applies one outcome for name and reports whether it was the
// DegradeAfter-th consecutive transient one (the count is then reset).
func (c *transientCounter) observe(name string, err error) bool {
	_, transient := provider.TransientOf(err)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !transient {
		delete(c.counts, name)
		return false
	}
	if c.counts == nil {
		c.counts = make(map[string]int)
	}
	c.counts[name]++
	if c.counts[name] < DegradeAfter {
		return false
	}
	delete(c.counts, name)
	return true
}

// healthObserver is the decorator-side handle: one provider, the shared
// counter and the writer.
type healthObserver struct {
	name    string
	counter *transientCounter
	health  provider.HealthManager
	log     *slog.Logger
}

// skipTransientKey marks a context whose calls the counter ignores.
type skipTransientKey struct{}

// WithoutTransientCount returns a child of ctx whose driver calls neither
// add to nor reset the consecutive-transient counter (a health probe's
// own call must not demote or rescue the provider it probes).
func WithoutTransientCount(ctx context.Context) context.Context {
	return context.WithValue(ctx, skipTransientKey{}, true)
}

// transientCountSkipped reports whether ctx carries WithoutTransientCount.
func transientCountSkipped(ctx context.Context) bool {
	skip, _ := ctx.Value(skipTransientKey{}).(bool)
	return skip
}

// observe feeds one call outcome and demotes on the threshold.
func (o *healthObserver) observe(ctx context.Context, err error) {
	if o == nil || o.health == nil || transientCountSkipped(ctx) {
		return
	}
	if !o.counter.observe(o.name, err) {
		return
	}
	if derr := o.health.DemoteProvider(context.WithoutCancel(ctx), o.name, provider.ReasonTransientUpstream); derr != nil {
		o.log.Warn("dispatch: provider demotion failed", slog.String("provider", o.name), slog.String("error", derr.Error()))
	}
}
