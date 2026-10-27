//go:build !windows

// Purpose: the daemon's boot-time config baseline check and the single-use
// loosening slot. checkBootBaseline runs in composeDaemon after the store
// and bus exist and before any background subsystem is built: a missing,
// unsigned-looser or divergent baseline writes the most-restrictive guarded
// values into cfg, so every later consumer (policy, hot reload, scheduler)
// sees only the effective config.
// Inputs: the loaded config, its file path, the runtime store and bus, the
// clock and the proof verifier (production: the custody-bound trust root).
// Outputs: a *configBaseline whose outcome the manifest reports as the
// "config-baseline" subsystem, and whose slot is the hot reloader's only
// loosening approver.
// Constraints: fail closed. A reload is approved only from slot state: a
// slot armed by config.accept_loosening, presented by the token that armed
// it (fsnotify and SIGHUP reloads carry none), unexpired, unconsumed, and
// for exactly the accepted guarded values. Approval consumes the slot.
// SPORT: cmd/cascade config baseline (P1-SEC-04).
package main

import (
	"context"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// configBaselineSubsystem is the manifest name status.get reports.
const configBaselineSubsystem = "config-baseline"

// loosenSlotTTL is how long an armed slot stays usable.
const loosenSlotTTL = 5 * time.Minute

// configBaseline is the boot outcome plus what config.accept_loosening needs.
type configBaseline struct {
	Outcome runtime.BaselineOutcome
	Detail  string
	Checker *runtime.BaselineChecker
	slot    *loosenSlot

	configPath string
	loadOpts   runtime.LoadOptions
	reloader   *runtime.HotReloader
}

// checkBootBaseline checks cfg against the recorded baseline and, when the
// outcome is not the on-disk view, writes the effective (most-restrictive)
// guarded values into cfg before returning. It cannot fail: every failure
// to prove a baseline is a fail-closed outcome, never an error that could
// be skipped.
func checkBootBaseline(ctx context.Context, cfg *runtime.Config, configPath string, store provider.Store, bus *events.Bus, clock runtime.Clock, verify runtime.BaselineVerifier) *configBaseline {
	var file runtime.BaselineFile
	if cf, err := runtime.ReadConfigFile(configPath); err == nil && cf.Found {
		file.SHA256 = cf.Digest()
		file.Signed = func() (runtime.EffectiveConfig, bool) {
			signed, err := cf.Parse(ctx, envFreeLoadOptions())
			if err != nil {
				return runtime.EffectiveConfig{}, false
			}
			return runtime.EffectiveConfigOf(signed), true
		}
	}
	checker := runtime.NewBaselineChecker(store, clock, &busEventPublisher{bus: bus}, runtime.NewStoreAuditRecorder(store, clock))
	checker.SetVerifier(verify)
	onDisk := runtime.EffectiveConfigOf(cfg)
	res := checker.CheckFile(ctx, onDisk, file)
	if runtime.ComputeSectionsHash(res.Effective) != runtime.ComputeSectionsHash(onDisk) {
		runtime.ApplyEffective(cfg, res.Effective)
	}
	return &configBaseline{
		Outcome: res.Outcome, Detail: res.Outcome.Detail(), Checker: checker,
		slot: &loosenSlot{clock: clock}, configPath: configPath,
	}
}

// report records the outcome on the manifest: an error state (status.get
// health "degraded") whenever the most-restrictive values run.
func (b *configBaseline) report(m *daemon.Manifest) {
	if b.Outcome.Degraded() {
		m.Failed(configBaselineSubsystem, b.Detail)
		return
	}
	m.Started(configBaselineSubsystem, b.Detail)
}

// attachReloader makes the slot the reloader's only loosening approver and
// keeps the reloader and its load options for config.accept_loosening.
func (b *configBaseline) attachReloader(hr *runtime.HotReloader, loadOpts runtime.LoadOptions) {
	if b == nil || hr == nil {
		return
	}
	hr.SetElevationApprover(b.slot)
	b.reloader, b.loadOpts = hr, loadOpts
}

// slotToken identifies one arming of the slot. Only the handler that armed
// it holds it; it is compared by pointer.
type slotToken struct{ _ byte }

type slotTokenKey struct{}

// withSlotToken attaches tok to ctx for one reload.
func withSlotToken(ctx context.Context, tok *slotToken) context.Context {
	return context.WithValue(ctx, slotTokenKey{}, tok)
}

// loosenSlot is the single-use, five-minute approval for exactly one set of
// accepted guarded values. The zero value with a clock is disarmed.
type loosenSlot struct {
	mu           sync.Mutex
	clock        runtime.Clock
	token        *slotToken
	sectionsHash string
	ref          string
	expires      time.Time
}

// arm replaces any earlier arming and returns the new token.
func (s *loosenSlot) arm(sectionsHash, ref string) *slotToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = &slotToken{}
	s.sectionsHash, s.ref = sectionsHash, ref
	s.expires = s.clock.Now().Add(loosenSlotTTL)
	return s.token
}

// ApproveLoosening implements runtime.ElevationApprover from slot state
// alone. Any presented token disarms the slot, approved or not.
func (s *loosenSlot) ApproveLoosening(ctx context.Context, proposed runtime.EffectiveConfig, _ []runtime.LooseningPath) (string, error) {
	tok, _ := ctx.Value(slotTokenKey{}).(*slotToken)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.token == nil:
		return "", cascade.New(cascade.KindElevationRequired, "no accepted loosening is pending; run config.accept_loosening")
	case tok == nil || tok != s.token:
		return "", cascade.New(cascade.KindElevationRequired, "this reload does not carry the accepted loosening")
	}
	ref, hash, expires := s.ref, s.sectionsHash, s.expires
	s.token, s.sectionsHash, s.ref = nil, "", ""
	if s.clock.Now().After(expires) {
		return "", cascade.New(cascade.KindElevationRequired, "the accepted loosening expired")
	}
	if runtime.ComputeSectionsHash(proposed) != hash {
		return "", cascade.New(cascade.KindConflict, "the config file changed after it was accepted")
	}
	return ref, nil
}

// withBaselineRuntime sets the runtime option's ConfigBaseline.
func withBaselineRuntime(opt rpcServerOption, b *configBaseline) rpcServerOption {
	if opt.runtime != nil {
		opt.runtime.ConfigBaseline = b
	}
	return opt
}

// recoverWithBaselineSettings re-resolves the daemon settings from cfg after
// checkBootBaseline rewrote its guarded values, so [sync] and every other
// setting carry only effective values, then runs the recovery scan with
// them. The settings loadDaemonConfig resolved before the check are never
// used again.
func recoverWithBaselineSettings(ctx context.Context, cfg *runtime.Config, paths runtime.PathProvider, deps daemonDeps, logProvider *runtime.LogProvider, bus *events.Bus, store provider.Store) (daemon.Settings, error) {
	settings, err := daemon.ResolveSettings(cfg, paths)
	if err != nil {
		return daemon.Settings{}, err
	}
	return settings, runRecoveryScan(ctx, paths, settings, deps, logProvider, bus, store)
}
