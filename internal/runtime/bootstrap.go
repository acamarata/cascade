package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"
)

// Purpose: assemble the runtime startup sequence — PathProvider, the TOML
//   loader (incl. the schema_version frame it owns), and profile
//   resolution — into a single entrypoint later subsystems call into.
// Inputs: BootstrapOptions carries every external input (the --profile
//   flag, injected env/home accessors, an injected Clock).
// Outputs: a *Runtime holding the resolved Profile, PathProvider, and
//   loaded *Config.
// Constraints: T-1 task 7 — this is the startup-sequence anchor that
//   S-04.T2, S-05.T3, and S-05.T4 change:-wire into; it must not grow
//   business logic beyond composing the pieces those tickets already
//   define. No vault/daemon dependency (elevation baseline enforcement is
//   C/S-05.T8's).
// SPORT: runtime/bootstrap (ADD, placeholder per T-1 sport_updates).

// BootstrapOptions carries every external input the bootstrap sequence
// needs. All fields are optional; a nil accessor falls back to the real
// process environment. Tests (Art.7.1) must always supply Getenv/HomeDir/
// Environ pointing at fakes.
type BootstrapOptions struct {
	ProfileFlag string
	Getenv      Getenv
	HomeDir     HomeDirFunc
	Environ     func() []string
	Clock       Clock
	Warn        func(format string, args ...interface{})
	// MetricsBus, if non-nil, starts the periodic metrics emitter
	// (P1-E03-W1-S05-T4) in its own goroutine, tied to ctx. The Registry
	// (Runtime.Metrics) is always created regardless — subsystems may
	// always register counters/gauges — but the emitter only runs, and
	// therefore only ever publishes, when a bus is supplied. A nil
	// MetricsBus is the honest default: no composition-root caller yet
	// constructs a real EventBus (see metrics_emitter.go's EventBus doc
	// for why cmd/cascade must supply an adapter over *events.Bus).
	MetricsBus EventBus
	// MetricsInterval overrides the emitter's tick period; zero uses
	// DefaultMetricsInterval (60s, per T-4's contract). Ignored when
	// MetricsBus is nil.
	MetricsInterval time.Duration
	// RecoveryLog receives the crash-safety recovery scanner's WARN
	// output (P1-E03-W1-S05-T3). A nil value falls back to
	// slog.Default() — see recovery.go's Scan for why this cannot wait
	// for the real LogProvider, which is itself constructed later in
	// this same function, after config is loaded.
	RecoveryLog *slog.Logger
	// RecoveryBus, if non-nil, receives the recovery scan's
	// RecoveryEvent (only when one is produced — see Scan's no-event-on-
	// clean-state contract). A nil value is the honest default: no
	// composition-root caller yet constructs a real EventBus here either
	// (see MetricsBus's doc comment above for the identical situation).
	RecoveryBus EventBus
	// RecoveryRegistry, if non-nil, is consulted for the recovery scan's
	// orphaned-advisory-lock step. See recovery.go's DomainRegistry doc
	// comment for why production wiring leaves this nil today
	// (CONTRACT-DEVIATIONS, this ticket's journal).
	RecoveryRegistry DomainRegistry
}

// Runtime is the fully resolved bootstrap result. Later tickets extend the
// startup sequence by consuming this struct — they must not construct
// PathProvider or Config themselves.
type Runtime struct {
	Profile Profile
	Paths   PathProvider
	Config  *Config
	Clock   Clock
	// Log is the *slog.Logger + rotation writer pairing (logger.go),
	// constructed from Config.Logging and Paths (S-04.T2 task 3). Later
	// subsystems receive it (or its .Logger()) via constructor
	// injection — never a package-global slog.SetDefault.
	Log *LogProvider
	// Metrics is the internal operational metrics registry
	// (P1-E03-W1-S05-T4). Always non-nil; subsystems register counters
	// and gauges against it via RegisterCounter/RegisterGauge. It is NOT
	// the telemetry egress system (H/S-16.T1) — see metrics.go.
	Metrics *Registry
}

// Close releases resources Bootstrap opened — currently just the log
// file. Safe to call on a *Runtime returned with a nil Log.
func (r *Runtime) Close() error {
	if r == nil || r.Log == nil {
		return nil
	}
	return r.Log.Close()
}

// Bootstrap runs the full startup sequence: resolve paths, load and
// validate config.toml (running the schema_version upgrade-rewrite frame
// when needed), and resolve the active profile. It is the single
// entrypoint every later subsystem's startup path calls into.
func Bootstrap(ctx context.Context, opts BootstrapOptions) (*Runtime, error) {
	clock := opts.Clock
	if clock == nil {
		clock = NewSystemClock()
	}

	paths, err := NewPathProvider(opts.Getenv, opts.HomeDir)
	if err != nil {
		return nil, fmt.Errorf("runtime: bootstrap paths: %w", err)
	}

	// Crash-safety recovery scan (P1-E03-W1-S05-T3): the FIRST startup
	// action after paths resolve, before config load and before any
	// domain lock acquisition, per §D-3 write-arbitration's
	// socket-probe-first contract. A live daemon on the socket aborts
	// Bootstrap immediately; a stale pidfile/socket/lock is cleaned up
	// and reported, never silently.
	// The *RecoveryEvent itself is deliberately discarded here: Bootstrap
	// has no consumer for it at this composition-root layer — Scan
	// already publishes it to opts.RecoveryBus internally when one is
	// supplied, so there is nothing further for Bootstrap's caller to do
	// with the return value. A stated choice, not an oversight, on the
	// same footing as internal/events/scheduler/retention_register.go's
	// discarded non-error return values from its own registered runnables.
	if _, err := Scan(ctx, RecoveryOptions{
		PidfilePath: filepath.Join(paths.Root(), "daemon.pid"),
		SocketPath:  paths.SocketPath(),
		Clock:       clock,
		Log:         opts.RecoveryLog,
		Bus:         opts.RecoveryBus,
		Registry:    opts.RecoveryRegistry,
	}); err != nil {
		return nil, fmt.Errorf("runtime: bootstrap recovery scan: %w", err)
	}

	cfg, err := Load(ctx, LoadOptions{
		Path:        paths.ConfigPath(),
		ProfileFlag: opts.ProfileFlag,
		Getenv:      opts.Getenv,
		Environ:     opts.Environ,
		Warn:        opts.Warn,
	})
	if err != nil {
		return nil, fmt.Errorf("runtime: bootstrap config: %w", err)
	}

	// runtime.home/runtime.data_dir are always the resolved PathProvider
	// values, stamped in here rather than read from the file (see
	// runtimeSection's doc comment in config.go).
	cfg.Runtime.Home = paths.Root()
	cfg.Runtime.DataDir = paths.DataDir()

	log, err := NewLogProvider(cfg.Logging, paths, clock)
	if err != nil {
		return nil, fmt.Errorf("runtime: bootstrap log: %w", err)
	}

	metrics := startMetrics(ctx, opts, clock)

	return &Runtime{
		Profile: cfg.Runtime.Profile,
		Paths:   paths,
		Config:  cfg,
		Clock:   clock,
		Log:     log,
		Metrics: metrics,
	}, nil
}

// startMetrics creates the always-available metrics Registry and, only
// when opts.MetricsBus is supplied, starts the periodic emitter goroutine
// in the background, tied to ctx. Split out of Bootstrap to keep both
// functions under Art.10.3's 50-line-per-function cap.
func startMetrics(ctx context.Context, opts BootstrapOptions, clock Clock) *Registry {
	metrics := NewRegistry()
	if opts.MetricsBus == nil {
		return metrics
	}
	interval := opts.MetricsInterval
	if interval <= 0 {
		interval = DefaultMetricsInterval
	}
	onError := func(error) {}
	if opts.Warn != nil {
		onError = func(err error) { opts.Warn("runtime: metrics emitter: %v", err) }
	}
	go RunPeriodicEmitter(ctx, PeriodicEmitterOptions{
		Registry: metrics,
		Clock:    clock,
		Ticker:   NewSystemTicker(interval),
		Bus:      opts.MetricsBus,
	}, onError)
	return metrics
}
