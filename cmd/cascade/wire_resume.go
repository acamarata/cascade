//go:build !windows

// Purpose: registers the fanout-resume daemon wiring (phaseConductor, after
//
//	the conductor): the three-way wiring rule over the conductor
//	registration's fan-out, the synchronous classify-only Scan before the
//	socket serves (status.get subsystem fanout-resume Started "pending N"),
//	and the supervised fanout-sweep that expires unattached fan-outs after
//	fanout_record_ttl, every fanout_sweep_interval.
//
// Inputs: daemonWiring (Store, Clock, ConductorFanOut, Deps.Config,
//
//	Manifest, Logger, Ctx).
//
// Outputs: the fanout-resume and fanout-sweep manifest entries.
// Constraints: Scan and Sweep get w.ConductorFanOut's own claim table, the
//
//	one the conductor.execute handler claims in; this file never builds a
//	second fan-out. No daemon store reports Skipped "no daemon store"; a
//	store with no claim table or journal and no error refuses startup
//	(resume.ErrConstructionFailed); an executor that failed to construct
//	(ConductorFanOut.Err) reports Skipped "pending (executor unavailable)",
//	scans nothing and keeps serving, and the sweep backstop still expires
//	unattached fan-outs whenever the claim table and journal exist (R8d m-3).
//
// SPORT: cmd/cascade daemon registrations (P1-CORE-15).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/resume"
	"github.com/acamarata/cascade/internal/runtime"
)

// The manifest names of the two fan-out resume subsystems.
const (
	fanOutResumeSubsystem = "fanout-resume"
	fanOutSweepSubsystem  = "fanout-sweep"
)

var _ = registerDaemonWiring(daemonRegistration{
	Name: "fanout-resume", Phase: phaseConductor, Order: 20,
	Wire: func(w *daemonWiring) error {
		return wireFanOutResume(w)
	},
})

// resumePlan is what the fanout-resume registration runs.
type resumePlan uint8

const (
	resumeNothing      resumePlan = iota // no store, or nothing to sweep with
	resumeSweepOnly                      // executor unavailable: no scan, the sweep backstop runs
	resumeScanAndSweep                   // the classify-only scan, then the sweep
)

// executorUnavailable is the fanout-resume detail when ConductorFanOut.Err
// is set.
const executorUnavailable = "pending (executor unavailable)"

// wireFanOutResume is the fanout-resume registration: settings, the
// three-way wiring rule, the synchronous classify-only Scan, then the
// supervised sweep.
func wireFanOutResume(w *daemonWiring) error {
	settings, err := daemon.ResolveResumeSettings(w.Deps.Config)
	if err != nil {
		return err
	}
	deps, plan, err := fanOutResumeDeps(w)
	if err != nil || plan == resumeNothing {
		return err
	}
	if plan == resumeScanAndSweep {
		scans, err := resume.Scan(w.Ctx, deps)
		if err != nil {
			return err
		}
		pending := 0
		for _, s := range scans {
			if s.Class == resume.FanOutResumable || s.Class == resume.FanOutCompleteUndelivered {
				pending++
			}
		}
		w.Manifest.Started(fanOutResumeSubsystem, fmt.Sprintf("pending %d", pending))
	}
	startFanOutSweep(w, deps, settings)
	return nil
}

// fanOutResumeDeps applies the three-way rule. No daemon store: Skipped,
// nothing runs. No claim table or journal: with no error that is a wiring
// bug (ErrConstructionFailed, startup refuses); with ConductorFanOut.Err
// set it is Skipped and nothing runs, since there is nothing to sweep with.
// Err set over a claim table and journal: Skipped, no scan, but the sweep
// still runs. Otherwise the scan and the sweep both run.
func fanOutResumeDeps(w *daemonWiring) (resume.FanOutDeps, resumePlan, error) {
	fan := w.ConductorFanOut
	switch {
	case w.Store == nil:
		w.Manifest.Skipped(fanOutResumeSubsystem, "no daemon store")
		return resume.FanOutDeps{}, resumeNothing, nil
	case fan.Claims == nil || fan.Journal == nil:
		if fan.Err == nil {
			return resume.FanOutDeps{}, resumeNothing, resume.ErrConstructionFailed
		}
		w.Manifest.Skipped(fanOutResumeSubsystem, executorUnavailable)
		return resume.FanOutDeps{}, resumeNothing, nil
	}
	deps := resume.FanOutDeps{Store: w.Store, Heads: fan.Journal, Claims: fan.Claims, Clock: w.Clock}
	if fan.Err != nil {
		w.Manifest.Skipped(fanOutResumeSubsystem, executorUnavailable)
		return deps, resumeSweepOnly, nil
	}
	return deps, resumeScanAndSweep, nil
}

// startFanOutSweep runs the sweep at startup and every SweepInterval as the
// supervised fanout-sweep subsystem under the daemon's run context.
func startFanOutSweep(w *daemonWiring, deps resume.FanOutDeps, s daemon.ResumeSettings) {
	detail := fmt.Sprintf("every %s, ttl %s", s.SweepInterval, s.RecordTTL)
	report := func(err error) {
		if err == nil {
			w.Manifest.Started(fanOutSweepSubsystem, detail)
			return
		}
		w.Manifest.Started(fanOutSweepSubsystem, detail+"; last sweep failed: "+err.Error())
		if w.Logger != nil {
			w.Logger.Error("fan-out sweep failed", slog.String("error", err.Error()))
		}
	}
	w.Manifest.GoSupervised(w.Ctx, fanOutSweepSubsystem, detail, func(ctx context.Context) error {
		return runFanOutSweep(ctx, deps, s.RecordTTL, runtime.NewSystemTicker(s.SweepInterval), report)
	})
}

// runFanOutSweep sweeps once, then once per tick, until ctx ends. A failed
// sweep is reported and the loop keeps going: one bad pass never stops the
// retention backstop.
func runFanOutSweep(ctx context.Context, deps resume.FanOutDeps, ttl time.Duration, ticker runtime.Ticker, report func(error)) error {
	defer ticker.Stop()
	for {
		if _, err := resume.Sweep(ctx, deps, deps.Clock.Now(), ttl); ctx.Err() == nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C():
		}
	}
}
