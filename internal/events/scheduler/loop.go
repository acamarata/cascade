package scheduler

// Purpose: RunLoop — the production driver behind Scheduler.Tick, the
//   "real time.Ticker in production composition-root wiring" scheduler.go
//   names but never itself provides. R-14.175 found RegisterRetentionJobs
//   wired to nothing; a Scheduler that is Activated but never Ticked is
//   the same inertness one layer up (persisted, orphan-checked, and
//   firing nothing), so the composition root needs this loop, not just
//   the registration call, to make retention genuinely run.
// Inputs: a *Scheduler already Activated, an internal/runtime.Ticker
//   (production: runtime.NewSystemTicker; tests: a fake that fires on
//   demand — the same abstraction internal/runtime's periodic metrics
//   emitter already uses, reused here rather than re-invented), and an
//   onError callback for a Tick error (never silently dropped, Art.1).
// Outputs: none directly — RunLoop returns when ctx is cancelled, having
//   called ticker.Stop() first.
// Constraints: no bare time.Now/time.NewTicker in this package — pacing
//   is entirely the injected Ticker's responsibility, matching
//   RunPeriodicEmitter's split between "when to fire" (Ticker) and "what
//   timestamp to record" (Clock, already threaded through Scheduler).
// SPORT: internal.events.scheduler.RunLoop/ADDED (R-14.175 wiring).

import (
	"context"

	"github.com/acamarata/cascade/internal/runtime"
)

// RunLoop calls s.Tick once per tick received from ticker, until ctx is
// cancelled. It stops ticker before returning. A non-nil error from Tick
// is reported to onError (if non-nil) and the loop continues — a single
// failed Tick (e.g. every job overrun) must not stop the scheduler from
// trying again on the next tick.
func RunLoop(ctx context.Context, s *Scheduler, ticker runtime.Ticker, onError func(error)) {
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			if _, err := s.Tick(ctx); err != nil && onError != nil {
				onError(err)
			}
		}
	}
}
