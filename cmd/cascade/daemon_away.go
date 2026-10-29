//go:build !windows

// Purpose: composes away mode over the daemon's one notify.Service: the
//
//	[notify] away keys, the replayed away journal, the attested presence
//	tracker, the fleet runtime's admission controller and the bus stall
//	source, ticked on the run context, with a validated config re-applied on
//	each accepted reload.
//
// Inputs: w.Deps.Notify, w.Deps.Presence, w.Deps.Fleet, w.Deps.Config,
//
//	w.Store, w.Bus, w.Paths, w.Clock, w.Logger and w.Ctx.
//
// Outputs: the "notify.away" manifest row (Started with its tick loop, or
//
//	Skipped with the reason), a stall-source loop and a reload loop.
//
// Constraints: any missing or invalid input records Skipped and builds
//
//	nothing, so no accumulation gate is ever switched on and nothing is
//	withheld. No second AdmissionController or Sampler exists: the fleet
//	runtime's is read. Every goroutine starts through Manifest.GoSupervised
//	with w.Ctx, so daemon stop joins them before the bus and store close. A
//	reload that fails to load or validate keeps the running config and
//	publishes notify.reload.rejected. The away journal has its own namespace
//	so the resume scan (default namespace) never sees an open episode.
//
// SPORT: cmd/cascade away mode composition (P1-HRN-11).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/runtime"
)

// Manifest rows and bus coordinates of away mode.
const (
	awaySubsystem        = "notify.away"
	awayStallSubsystem   = "notify.away.stalls"
	awayReloadSubsystem  = "notify.away.reload"
	awayJournalNamespace = "notify.away"
	awayEntityID         = "local"
	awayReloadCursor     = "notify-away-reload"
	awayReloadBuffer     = 16
	awayTickInterval     = 30 * time.Second
	// awayReloadAccepted is the kind the hot reloader publishes on the
	// "daemon" namespace (hotReloadEventNamespace) once it accepts a file.
	awayReloadAccepted events.EventKind = "config.reload.accepted"
	// awayReloadRejected is published on the "notify" namespace when a
	// reload's away keys are refused.
	awayReloadRejected events.EventKind = "notify.reload.rejected"
)

// newAwayController builds the controller; a test counts and inspects calls.
// A journal with no open episode starts the controller fresh (Active);
// otherwise it installs the replayed state.
var newAwayController = func(deps notify.AwayDeps, cfg notify.AwayConfig, restored notify.ReplayResult) *notify.AwayController {
	if restored.State == notify.StateActive {
		return notify.NewAwayController(deps, cfg)
	}
	return notify.NewAwayControllerFrom(deps, cfg, restored)
}

// awayTicker paces the tick loop; a test swaps in a stepped ticker.
var awayTicker = func() runtime.Ticker { return runtime.NewSystemTicker(awayTickInterval) }

// awayMissing names the first dependency w lacks, or "".
func awayMissing(w *daemonWiring) string {
	switch {
	case w.Deps.Notify == nil:
		return "missing dependency: notify runtime"
	case w.Deps.Presence == nil:
		return "missing dependency: operator presence tracker"
	case w.Deps.Fleet == nil || w.Deps.Fleet.Admission() == nil:
		return "missing dependency: fleet admission controller"
	case w.Store == nil:
		return "missing dependency: store for the away journal"
	case w.Bus == nil:
		return "missing dependency: event bus for the stall source"
	}
	return ""
}

// awayConfigOf reads the away keys out of cfg.Extra["notify"]. A nil config
// is the absent section.
func awayConfigOf(cfg *runtime.Config) (notify.AwayConfig, error) {
	if cfg == nil {
		return notify.ParseAwaySection(nil)
	}
	return notify.ParseAwaySection(cfg.Extra["notify"])
}

// wireAwayMode builds away mode and starts its loops, or records why not.
func wireAwayMode(w *daemonWiring) error {
	if reason := awayMissing(w); reason != "" {
		w.Manifest.Skipped(awaySubsystem, reason)
		return nil
	}
	cfg, err := awayConfigOf(w.Deps.Config)
	if err != nil {
		w.Manifest.Skipped(awaySubsystem, err.Error())
		return nil
	}
	js := journal.New(w.Store, w.Clock, awayJournalNamespace)
	restored, err := notify.ReplayState(w.Ctx, js, awayEntityID)
	if err != nil {
		w.Manifest.Skipped(awaySubsystem, "away journal replay failed: "+err.Error())
		return nil
	}
	stalls, err := notify.NewBusStallSource(w.Ctx, w.Bus, w.Logger)
	if err != nil {
		w.Manifest.Skipped(awaySubsystem, "stall source unavailable: "+err.Error())
		return nil
	}
	reloads, err := w.Bus.SubscribeFromHead(w.Ctx, hotReloadEventNamespace, awayReloadCursor, awayReloadBuffer)
	if err != nil {
		_ = stalls.Run(cancelledContext()) // stops the stall subscription it opened
		w.Manifest.Skipped(awaySubsystem, "reload subscription unavailable: "+err.Error())
		return nil
	}
	ctrl := newAwayController(awayDeps(w, js, stalls, cfg), cfg, restored)
	startAwayLoops(w, ctrl, stalls, reloads)
	return nil
}

// cancelledContext returns a cancelled context: BusStallSource.Run on it
// stops the source's subscription and returns at once.
func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// awayDeps gathers the controller's collaborators from the wiring.
func awayDeps(w *daemonWiring, js journal.Store, stalls notify.StallSource, cfg notify.AwayConfig) notify.AwayDeps {
	svc := w.Deps.Notify.Service()
	newDigest := func(cfg notify.AwayConfig) *notify.DigestCompiler {
		return notify.NewDigestCompiler(svc.Registry, svc.Notifier, svc.Router, w.Clock, cfg, w.Logger)
	}
	return notify.AwayDeps{
		Router: svc.Router, Digest: newDigest(cfg), NewDigest: newDigest,
		Presence: w.Deps.Presence, Stalls: stalls, Admission: w.Deps.Fleet.Admission(),
		Clock: w.Clock, Journal: js, EntityID: awayEntityID, Log: w.Logger,
	}
}

// startAwayLoops runs the tick, stall and reload loops as supervised
// subsystems on the run context.
func startAwayLoops(w *daemonWiring, ctrl *notify.AwayController, stalls *notify.BusStallSource, reloads *events.Subscription) {
	w.Manifest.GoSupervised(w.Ctx, awaySubsystem, "evaluating operator absence", func(ctx context.Context) error {
		return runAwayTicks(ctx, ctrl, awayTicker())
	})
	w.Manifest.GoSupervised(w.Ctx, awayStallSubsystem, "reading supervision.stalled", stalls.Run)
	r := &awayReloader{ctrl: ctrl, bus: w.Bus, sub: reloads, load: awayConfigLoader(w), log: w.Logger}
	w.Manifest.GoSupervised(w.Ctx, awayReloadSubsystem, "applying [notify] reloads", r.run)
}

// runAwayTicks ticks the controller until ctx ends, then stops the ticker. A
// panic fails delivery open first (nothing is left to end an away episode),
// then propagates so the supervisor records the subsystem as failed.
func runAwayTicks(ctx context.Context, ctrl *notify.AwayController, t runtime.Ticker) error {
	defer t.Stop()
	defer func() {
		if v := recover(); v != nil {
			ctrl.FailOpen()
			panic(v)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C():
			ctrl.Tick(ctx)
		}
	}
}

// awayConfigLoader re-reads config.toml the way the hot reloader does.
func awayConfigLoader(w *daemonWiring) func(context.Context) (*runtime.Config, error) {
	opts := runtime.LoadOptions{Getenv: w.Deps.Daemon.Getenv, Environ: w.Deps.Daemon.Environ}
	if w.Paths != nil {
		opts.Path = w.Paths.ConfigPath()
	}
	if w.Logger != nil {
		opts.Warn = func(format string, args ...interface{}) { w.Logger.Warn(fmt.Sprintf(format, args...)) }
	}
	return func(ctx context.Context) (*runtime.Config, error) { return runtime.Load(ctx, opts) }
}

// awayReloader applies each accepted config reload to the running controller.
type awayReloader struct {
	ctrl *notify.AwayController
	bus  *events.Bus
	sub  *events.Subscription
	load func(context.Context) (*runtime.Config, error)
	log  *slog.Logger
}

// run reads the daemon namespace until ctx ends or the subscription fails.
func (r *awayReloader) run(ctx context.Context) error {
	defer func() { _ = r.sub.Unsubscribe() }()
	errs := r.sub.Errs
	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-errs:
			if !ok {
				errs = nil // a closed channel would win every select
			} else if err != nil {
				return err
			}
		case ev, ok := <-r.sub.Events:
			if !ok {
				return nil
			}
			if ev.Kind == awayReloadAccepted {
				r.apply(ctx)
			}
		}
	}
}

// apply re-reads the file and installs its away keys. Any failure keeps the
// running config and publishes the rejection.
func (r *awayReloader) apply(ctx context.Context) {
	cfg, err := r.load(ctx)
	if err == nil {
		var away notify.AwayConfig
		if away, err = awayConfigOf(cfg); err == nil {
			err = r.ctrl.SetConfig(away)
		}
	}
	if err == nil {
		if r.log != nil {
			now := r.ctrl.Config()
			r.log.Info("notify: away config applied", "away_threshold", now.Threshold.String(), "digest_urgent_deeplinks", now.DigestUrgentDeepLinks)
		}
		return
	}
	if r.log != nil {
		r.log.Warn("notify: away config reload rejected, keeping the running config", "error", err.Error())
	}
	body, _ := json.Marshal(map[string]string{"error": err.Error()})
	_, _ = r.bus.Publish(ctx, daemon.NotifyNamespace, awayReloadRejected, awaySubsystem, body)
}
