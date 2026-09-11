// Purpose: Scheduler (P1-E10-W3-S20-T3), the background probe goroutine:
//   a lazy scan of ListProviders, probing each provider whose computed
//   next-probe delay has elapsed, on the R-14.33 per-status interval
//   schedule with exponential backoff.
//
// NEXT-PROBE-AT STATE (recorded, mirrors probe.go's BACKOFF STATE note).
// The frozen S-20.T2 schema has no next_probe_at column, so "ListProviders
// sorted by next_probe_at" is read as: this Scheduler tracks next-probe
// deadlines itself, process-local, and skips any provider not yet due on
// each scan pass -- functionally equivalent to a sorted scan without
// requiring a persisted, sortable column the frozen schema does not carry.
//
// Inputs: a *Manager, the same *registry.Registry, an injected
//   runtime.Clock, and an Intervals schedule.
// Outputs: RecoverProbe/GetHealth calls against due providers; no direct
//   registry writes (those happen inside Manager, already tested there).
// Constraints: cancellable via context.Context (Run returns as soon as
//   ctx ends); every wait uses time.NewTimer, never a bare time.Sleep/
//   After (R-14.132 exempts NewTimer/Ticker from the bare-time-call gate;
//   see internal/plugins/process/restart.go's backoffSleep for the
//   identical, already-landed pattern this file mirrors).
// SPORT: provider.health/ADD (P1-E10-W3-S20-T3).

package health

import (
	"context"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
)

// Intervals is the R-14.33 ratified per-status probe schedule.
type Intervals struct {
	Healthy      time.Duration
	DegradedBase time.Duration
	DeadBase     time.Duration
	DeadMax      time.Duration
}

// DefaultIntervals returns R-14.33's ratified defaults: healthy 300s,
// degraded 60s (exponential backoff per demotion_count), dead 900s
// (doubling per failed recovery, capped at 3600s).
func DefaultIntervals() Intervals {
	return Intervals{
		Healthy: 300 * time.Second, DegradedBase: 60 * time.Second,
		DeadBase: 900 * time.Second, DeadMax: 3600 * time.Second,
	}
}

// maxBackoffShift bounds the exponential shift so a large demotion/backoff
// counter can never overflow time.Duration (an int64 nanosecond count).
const maxBackoffShift = 10

// nextInterval computes the next-probe delay for one provider's current
// status. Exported (lowercase-package-private is fine, but this is the
// "backoff-schedule golden (frozen clock)" acceptance criterion's direct
// target) as a pure function so scheduler_test.go can assert an exact
// doubling-step table with no goroutine or clock plumbing involved.
func nextInterval(status registry.HealthStatus, demotionCount, recoveryFailures int, iv Intervals) time.Duration {
	switch status {
	case registry.HealthDegraded:
		return iv.DegradedBase * time.Duration(int64(1)<<uint(clampShift(demotionCount-1)))
	case registry.HealthDead:
		d := iv.DeadBase * time.Duration(int64(1)<<uint(clampShift(recoveryFailures)))
		if d > iv.DeadMax || d <= 0 {
			d = iv.DeadMax
		}
		return d
	case registry.HealthUnknown, registry.HealthHealthy:
		return iv.Healthy
	default:
		return iv.Healthy
	}
}

func clampShift(n int) int {
	if n < 0 {
		return 0
	}
	if n > maxBackoffShift {
		return maxBackoffShift
	}
	return n
}

// Scheduler is the background probe goroutine's owner. The zero value is
// not usable; construct with NewScheduler.
type Scheduler struct {
	mgr       *Manager
	reg       *registry.Registry
	clock     runtime.Clock
	intervals Intervals

	mu   sync.Mutex
	next map[string]time.Time
}

// NewScheduler returns a ready-to-use Scheduler.
func NewScheduler(mgr *Manager, reg *registry.Registry, clock runtime.Clock, intervals Intervals) *Scheduler {
	return &Scheduler{mgr: mgr, reg: reg, clock: clock, intervals: intervals, next: make(map[string]time.Time)}
}

// Run scans and probes every due provider once per scanEvery tick, until
// ctx is done. Intended to run in its own goroutine, started and canceled
// by the daemon.
func (s *Scheduler) Run(ctx context.Context, scanEvery time.Duration) {
	for {
		s.scanOnce(ctx)
		if !waitOrDone(ctx, scanEvery) {
			return
		}
	}
}

// scanOnce probes every currently-due provider once, in ListProviders'
// name order.
func (s *Scheduler) scanOnce(ctx context.Context) {
	recs, err := s.reg.ListProviders(ctx)
	if err != nil {
		return
	}
	now := s.clock.Now()
	for _, rec := range recs {
		s.mu.Lock()
		due, seen := s.next[rec.Name]
		s.mu.Unlock()
		if seen && now.Before(due) {
			continue
		}
		s.probeOne(ctx, rec)
	}
}

// probeOne runs one scheduled check for rec: RecoverProbe for a degraded
// or dead provider, a lightweight GetHealth for anything else, then
// reschedules rec's next-probe deadline from the (possibly just-updated)
// status.
func (s *Scheduler) probeOne(ctx context.Context, rec registry.ProviderRecord) {
	switch rec.HealthStatus {
	case registry.HealthDegraded, registry.HealthDead:
		_, _ = s.mgr.RecoverProbe(ctx, rec.Name)
	case registry.HealthUnknown, registry.HealthHealthy:
		_, _ = s.mgr.GetHealth(ctx, rec.Name)
	default:
		_, _ = s.mgr.GetHealth(ctx, rec.Name)
	}

	status, demotionCount := rec.HealthStatus, rec.DemotionCount
	if updated, err := s.reg.GetProvider(ctx, rec.Name); err == nil {
		status, demotionCount = updated.HealthStatus, updated.DemotionCount
	}
	interval := nextInterval(status, demotionCount, s.mgr.BackoffSteps(rec.Name), s.intervals)

	s.mu.Lock()
	s.next[rec.Name] = s.clock.Now().Add(interval)
	s.mu.Unlock()
}

// waitOrDone blocks for d or until ctx is done, whichever comes first,
// returning false iff ctx ended the wait first.
func waitOrDone(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
