package daemon

// Purpose: the daemon's one fleet runtime. NewFleetRuntime runs ordered
//
//	builder steps and owns what they build. The first step builds the ONE
//	governor.AdmissionController over a governor.Sampler run as the supervised
//	subsystem "governor-sampler" on the run context, configured from
//	[governor], and keeps the manifest row "admission" at its signal posture
//	so status.get reports it.
//
// Inputs: FleetRuntimeDeps. Outputs: a *FleetRuntime read as w.Deps.Fleet.
// Constraints: no second AdmissionController or Sampler, no bare go statement
//
//	(Manifest.GoSupervised only), no change to the admission decision logic.
//	A later ticket adds ONE step in NewFleetRuntime plus its own
//	subsystems_fleet_<part>.go file, and registers teardown with addCloser.
//	Manifest.Wait returns once the sampler's ticker has stopped, the last call
//	of its loop; the loop goroutine's own exit needs a governor Done signal.
// SPORT: internal/daemon (ADD, P1-CAP-12; capacity step P1-CAP-05).

import (
	"context"
	"errors"
	"log/slog"
	goruntime "runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// The manifest rows the admission step owns.
const (
	fleetSamplerSubsystem   = "governor-sampler"
	fleetAdmissionSubsystem = "admission"
	fleetRowSubsystem       = "admission-posture"
)

// Test seams: the row ticker, a sampler-start hook, the host collector (nil in production).
var (
	newRowTicker       = func(period time.Duration) runtime.Ticker { return runtime.NewSystemTicker(period) }
	beforeSamplerStart = func(*FleetRuntime) {}
	samplerCollect     func() (governor.ResourceSnapshot, error)
)

// FleetRuntimeDeps is everything a builder step may read. Every field but
// SamplerTicker and Logger is required.
type FleetRuntimeDeps struct {
	Paths    runtime.PathProvider
	Clock    runtime.Clock
	Manifest *Manifest
	Bus      *events.Bus
	Config   *runtime.Config
	// Store is the daemon store: the fleet journal and attention views.
	Store   provider.Store
	Binding nodes.ControllerBindingBackend
	// SamplerTicker drives the sampler; nil means the system ticker.
	SamplerTicker runtime.Ticker
	Logger        *slog.Logger
}

// fleetStep is one named builder step of NewFleetRuntime.
type fleetStep struct {
	name  string
	build func(ctx context.Context, deps FleetRuntimeDeps) error
}

// FleetRuntime owns the daemon's fleet-level services. Build it once with
// NewFleetRuntime; every method is safe for concurrent use.
type FleetRuntime struct {
	manifest  *Manifest
	admission *governor.AdmissionController
	sampler   *governor.Sampler
	ticker    *samplerTicker
	// postureOverride is a test seam: when set, it replaces the controller's posture.
	postureOverride atomic.Pointer[governor.SignalPosture]

	mu      sync.Mutex
	closers []func() error
	// The body starts the sampler only while !closed; Close stops an unstarted ticker.
	started, closed bool
	rowState        SubsystemState
	rowDetail       string

	closeOnce sync.Once
	closeErr  error
	res       fleetReservation // the reservation step (subsystems_fleet_reservation.go)
	capa      fleetCapacity    // the capacity step (subsystems_fleet_capacity.go)
}

// NewFleetRuntime validates deps, then runs the builder steps in order. The
// first failing step closes what was built and its error is returned wrapped
// with the step name, keeping the cause's Kind.
func NewFleetRuntime(ctx context.Context, deps FleetRuntimeDeps) (*FleetRuntime, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	r := &FleetRuntime{manifest: deps.Manifest}
	steps := []fleetStep{
		{name: "admission", build: r.buildAdmission},
		{name: "reservation", build: r.buildReservation},
		{name: "capacity", build: r.buildCapacity},
	}
	for _, step := range steps {
		if err := step.build(ctx, deps); err != nil {
			_ = r.Close()
			kind, ok := cascade.KindOf(err)
			if !ok {
				kind = cascade.KindInternal
			}
			return nil, cascade.Wrapf(kind, err, "fleet runtime: step %q", step.name)
		}
	}
	return r, nil
}

// validate refuses a nil required dependency with KindInvalidInput.
func (d FleetRuntimeDeps) validate() error {
	for _, dep := range []struct {
		name    string
		missing bool
	}{
		{"Paths", d.Paths == nil}, {"Clock", d.Clock == nil}, {"Manifest", d.Manifest == nil},
		{"Bus", d.Bus == nil}, {"Config", d.Config == nil}, {"Store", d.Store == nil}, {"Binding", d.Binding == nil},
	} {
		if dep.missing {
			return cascade.Newf(cascade.KindInvalidInput, "fleet runtime: required dependency %s is nil", dep.name)
		}
	}
	return nil
}

// Admission returns the daemon's one admission controller.
func (r *FleetRuntime) Admission() *governor.AdmissionController { return r.admission }

// addCloser registers a teardown for what a builder step opened; Close runs
// the closers in reverse order.
func (r *FleetRuntime) addCloser(fn func() error) {
	r.mu.Lock()
	r.closers = append(r.closers, fn)
	r.mu.Unlock()
}

// Close stops the sampler (the controller then refuses new and queued
// admissions) and runs the registered closers in reverse. It is idempotent,
// safe before the sampler body starts (the body then never starts it) and
// returns the first call's joined error. Callers run it after Manifest.Wait
// and before the store closes.
func (r *FleetRuntime) Close() error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		started, closers := r.started, append([]func() error(nil), r.closers...)
		r.mu.Unlock()
		if r.sampler != nil {
			r.sampler.Stop()
		}
		if r.ticker != nil && !started {
			r.ticker.Stop() // never started: the sampler loop will not stop it
		}
		var errs []error
		for i := len(closers) - 1; i >= 0; i-- {
			errs = append(errs, closers[i]())
		}
		r.closeErr = errors.Join(errs...)
		goruntime.Gosched() // lets the sampler goroutine finish returning (test hygiene)
	})
	return r.closeErr
}

// buildAdmission is the admission step: [governor] config, the one sampler,
// the one controller, the supervised goroutines and the posture row.
func (r *FleetRuntime) buildAdmission(ctx context.Context, d FleetRuntimeDeps) error {
	// DEBT (fail-small): nothing is sampled yet, so Load calibrates an unpinned
	// preset on a zero snapshot, which is the minimal envelope, and the
	// controller is never recalibrated. Pin [governor].preset or the admission
	// keys to choose otherwise. A synchronous first sample or a reconfigure
	// needs a governor API.
	admissionCfg, _, err := governor.Load(ctx, d.Paths.ConfigPath(), governor.ResourceSnapshot{})
	if err != nil {
		return err
	}
	inner := d.SamplerTicker
	if inner == nil {
		inner = runtime.NewSystemTicker(governor.PeriodForHz(0))
	}
	r.ticker = &samplerTicker{inner: inner, onWait: r.refreshAdmission, stopped: make(chan struct{})}
	r.sampler = governor.NewSampler(governor.SamplerConfig{Collect: samplerCollect}, d.Clock, r.ticker, d.Logger)
	r.admission = governor.NewAdmissionController(r.sampler, admissionCfg, d.Clock)
	d.Manifest.Register(fleetAdmissionSubsystem)
	r.refreshAdmission()
	d.Manifest.GoSupervised(ctx, fleetSamplerSubsystem, "sampling resource signals", r.superviseSampler)
	d.Manifest.GoSupervised(ctx, fleetRowSubsystem, "refreshing the admission row", r.superviseRow(newRowTicker(r.sampler.Period())))
	return nil
}

// superviseSampler starts the sampler on the run context unless Close already
// ran, then returns once the sampler's loop has stopped its ticker (the run
// context ended, or Close stopped it). Any other context would leave this
// body, and so Manifest.Wait, blocked.
func (r *FleetRuntime) superviseSampler(ctx context.Context) error {
	beforeSamplerStart(r)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.started = true
	r.sampler.Start(ctx)
	r.mu.Unlock()
	<-r.ticker.stopped
	return nil
}

// superviseRow refreshes the admission row on every row tick, so a sampler
// wedged inside a collection (it never returns to C()) still shows its
// posture ageing to stale. It ends with the run context or the sampler.
func (r *FleetRuntime) superviseRow(t runtime.Ticker) func(context.Context) error {
	return func(ctx context.Context) error {
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-r.ticker.stopped:
				return nil
			case <-t.C():
				r.refreshAdmission()
			}
		}
	}
}

// refreshAdmission writes the current posture to the admission row (see
// admissionRow). The posture is read and the row written under one lock, so
// two refreshers never reorder rows; unchanged values write nothing.
func (r *FleetRuntime) refreshAdmission() {
	r.mu.Lock()
	defer r.mu.Unlock()
	var posture governor.SignalPosture
	if p := r.postureOverride.Load(); p != nil {
		posture = *p
	} else {
		posture, _ = r.admission.Posture()
	}
	state, detail := admissionRow(posture)
	if r.rowDetail == detail && r.rowState == state {
		return
	}
	r.rowState, r.rowDetail = state, detail
	if state == SubsystemError {
		r.manifest.Failed(fleetAdmissionSubsystem, detail)
		return
	}
	r.manifest.Started(fleetAdmissionSubsystem, detail)
}

// admissionRow maps a signal posture to the admission row: running with
// "posture=<posture>", except no-signal, an error row (health degraded) that
// names the refusal. Stale stays a running row on purpose: the ticket's
// acceptance reads "Started with detail posture=<posture>" and only no-signal
// changes the state, and every daemon is stale until its first sample, so a
// degraded stale row would read unhealthy on every boot.
func admissionRow(posture governor.SignalPosture) (SubsystemState, string) {
	detail := "posture=" + string(posture)
	if posture == governor.PostureNoSignal {
		return SubsystemError, detail + ": " + governor.ErrNoResourceSignal.Error()
	}
	return SubsystemRunning, detail
}

// samplerTicker wraps the sampler's ticker. The sampler loop asks for C() at
// the top of every iteration, right after the previous tick and its hooks
// finished, so onWait sees each completed sample with no goroutine of its
// own. Stop signals stopped after the inner ticker has stopped (the system
// ticker's Stop joins its pump). The loop calls Stop as it exits.
type samplerTicker struct {
	inner   runtime.Ticker
	onWait  func()
	stopped chan struct{}
	once    sync.Once
}

// C reports the completed sample, then returns the inner tick channel.
func (t *samplerTicker) C() <-chan struct{} {
	t.onWait()
	return t.inner.C()
}

// Stop stops the inner ticker and signals stopped. Safe to call repeatedly.
func (t *samplerTicker) Stop() {
	t.inner.Stop()
	t.once.Do(func() { close(t.stopped) })
}
