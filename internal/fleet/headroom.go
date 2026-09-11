// Package fleet (headroom.go) implements the fleet headroom model
// (P1-E18-W4-S40-T2): a per-resource admission-capacity reading computed
// from the governor's live admitted-job count and the throttle ladder's
// CURRENTLY enforced ceiling - never the static config maximum.
//
// BINDING-CEILING HONESTY: HeadroomModel denominates against whatever
// admission ceiling is actually in force this instant (governor.
// AdmissionController.EnforcedCeiling, which already folds in
// StageCritical's halving and StageHalt's true zero-additional-room
// reading), never AdmissionConfig.MaxInflight's static value. A system
// throttled down to a lower real ceiling reports headroom against that
// lower number, so a caller never sees capacity that Admit would refuse.
//
// Ratio is REMAINING headroom, not saturation: (ceiling-admitted)/ceiling,
// 1.0 fully idle, 0.0 saturated at the ceiling in force right now. This
// resolves a contradiction in the ticket's own text: full_desc states the
// formula as "admitted / enforced_ceiling" (which would read 1.0 at full
// capacity), while acceptance_criteria explicitly requires "Full-capacity
// (admitted==enforced_ceiling): ratio=0.0". The two cannot both hold under
// one formula; this implementation follows acceptance_criteria (the
// testable, CI-enforced contract) and the "Headroom" name itself (room
// remaining, not room used) - see journals/P1-E18-W4-S40-T2.md for the
// full contradiction record.
//
// FAIL CLOSED: an enforced_ceiling of zero (or a nil AdmissionState/
// CeilingSource) is refused with ErrHeadroomUnavailable rather than
// silently defaulting to "unlimited" or "fine" - a missing or
// unparseable capacity signal must never read as plenty of room.
//
// SEAM: census state is read only through the L/S-25.T4 census.Reader
// interface (Latest/Count over []census.Snapshot), never the concrete
// Collector or its poller directly.
//
// SPORT: internal/fleet.HeadroomModel (ADD, P1-E18-W4-S40-T2).
package fleet

import (
	"context"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/fleet/census"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// HeadroomResourceInflight is the sole resource axis this ticket
// publishes: admission in-flight weight against the governor's enforced
// ceiling. A future resource axis (e.g. per-lane) is additive, not a
// change to this constant's meaning.
const HeadroomResourceInflight = "inflight"

// ErrHeadroomUnavailable is returned when the enforced ceiling cannot be
// honestly read: zero, or no ceiling source installed at all. Callers
// must treat this as "no headroom", never as "unlimited".
var ErrHeadroomUnavailable = cascade.New(cascade.KindInvalidInput,
	"fleet: enforced admission ceiling is zero or unknown; refusing to report headroom")

// AdmissionState is the seam HeadroomModel reads the live admitted-job
// count from. Satisfied by *governor.AdmissionController.
type AdmissionState interface {
	// Inflight returns the current sum of admitted, unreleased weights.
	Inflight() int
}

// CeilingSource is the seam HeadroomModel reads the throttle ladder's
// CURRENTLY enforced admission ceiling from - binding-ceiling honesty's
// denominator, never AdmissionConfig.MaxInflight's static value. Satisfied
// by *governor.AdmissionController.EnforcedCeiling.
type CeilingSource interface {
	// EnforcedCeiling returns the admission ceiling in force right now.
	EnforcedCeiling() int
}

// Headroom is one resource's admission-capacity reading at one instant.
type Headroom struct {
	// Resource names the capacity axis this reading covers, e.g.
	// HeadroomResourceInflight.
	Resource string
	// Admitted is the live admitted-job count (AdmissionState.Inflight).
	Admitted uint64
	// EnforcedCeiling is the throttle-ladder-binding ceiling in force
	// right now (CeilingSource.EnforcedCeiling) - never the static config
	// maximum.
	EnforcedCeiling uint64
	// Ratio is remaining headroom in [0, 1]: (EnforcedCeiling-Admitted) /
	// EnforcedCeiling. 1.0 is fully idle; 0.0 is saturated at the ceiling
	// actually enforced this instant.
	Ratio float64
	// CensusCount is the last stored census.Reader process count,
	// published alongside the admission-derived ratio for observability;
	// it does not participate in the ratio computation.
	CensusCount int
}

// HeadroomModel computes and republishes fleet admission headroom.
type HeadroomModel interface {
	// Compute returns the current Headroom reading, or
	// ErrHeadroomUnavailable when the enforced ceiling cannot be
	// honestly read.
	Compute() (Headroom, error)
}

// gaugeSet is the C-S05.T4 counters/gauges snapshot API surface a
// HeadroomPublisher writes to, so downstream consumers (S-40.T1's TUI,
// S-40.T4's RPC/SSE) read published headroom without importing
// internal/fleet directly.
type gaugeSet struct {
	admitted    *runtime.Gauge
	ceiling     *runtime.Gauge
	ratioMilli  *runtime.Gauge // ratio * 1000, Gauge is int64-only
	censusCount *runtime.Gauge
}

// HeadroomPublisher implements HeadroomModel and, on a tick, republishes
// its reading to an injected *runtime.Registry. The zero value is not
// usable; construct with NewHeadroomPublisher.
type HeadroomPublisher struct {
	admission AdmissionState
	ceiling   CeilingSource
	censusR   census.Reader
	clk       runtime.Clock
	ticker    runtime.Ticker

	gaugeOnce sync.Once
	gauges    gaugeSet

	cancelMu sync.Mutex
	cancel   context.CancelFunc

	// afterTick is a test-only hook, invoked once per tick after
	// publication. Always nil in production; mirrors governor's
	// sampler.go/throttle.go afterTick convention (Art.7.3 - no sleep-
	// based test synchronization).
	afterTick func()
}

// NewHeadroomPublisher builds a HeadroomPublisher reading admitted count
// from admission and the binding ceiling from ceiling. admission and
// ceiling may be nil only to exercise the fail-closed path; production
// always supplies a live *governor.AdmissionController for both (it
// satisfies both seams). censusR may be nil - CensusCount then always
// reads 0, a documented "nothing stored yet" state distinct from a
// missing Reader entirely (Reader.Count already defines that zero as its
// own before-any-Store default). reg is the C-S05.T4 snapshot API this
// publisher writes gauges into; reg must not be nil in production. clk
// and ticker default to the system implementations when nil; tests must
// always inject a runtime.FixedClock and a fake Ticker (Art.7.3).
func NewHeadroomPublisher(admission AdmissionState, ceiling CeilingSource, censusR census.Reader, reg *runtime.Registry, clk runtime.Clock, ticker runtime.Ticker) *HeadroomPublisher {
	if clk == nil {
		clk = runtime.NewSystemClock()
	}
	if ticker == nil {
		ticker = runtime.NewSystemTicker(time.Second)
	}
	p := &HeadroomPublisher{admission: admission, ceiling: ceiling, censusR: censusR, clk: clk, ticker: ticker}
	if reg != nil {
		p.gauges = gaugeSet{
			admitted:    reg.RegisterGauge("fleet_headroom_admitted", map[string]string{"resource": HeadroomResourceInflight}),
			ceiling:     reg.RegisterGauge("fleet_headroom_enforced_ceiling", map[string]string{"resource": HeadroomResourceInflight}),
			ratioMilli:  reg.RegisterGauge("fleet_headroom_ratio_milli", map[string]string{"resource": HeadroomResourceInflight}),
			censusCount: reg.RegisterGauge("fleet_headroom_census_count", map[string]string{"resource": HeadroomResourceInflight}),
		}
	}
	return p
}

// Compute returns the current Headroom reading. A nil admission or
// ceiling seam, or a zero (or negative) EnforcedCeiling, is refused with
// ErrHeadroomUnavailable - never treated as unlimited headroom.
func (p *HeadroomPublisher) Compute() (Headroom, error) {
	if p.admission == nil || p.ceiling == nil {
		return Headroom{}, ErrHeadroomUnavailable
	}
	ceil := p.ceiling.EnforcedCeiling()
	if ceil <= 0 {
		return Headroom{}, ErrHeadroomUnavailable
	}
	admitted := p.admission.Inflight()
	if admitted < 0 {
		admitted = 0
	}
	ratio := 1 - float64(admitted)/float64(ceil)
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	censusCount := 0
	if p.censusR != nil {
		censusCount = p.censusR.Count()
	}
	return Headroom{
		Resource:        HeadroomResourceInflight,
		Admitted:        uint64(admitted),
		EnforcedCeiling: uint64(ceil),
		Ratio:           ratio,
		CensusCount:     censusCount,
	}, nil
}

// Start launches the publisher's background tick loop, which recomputes
// and republishes Headroom on every tick until ctx is cancelled or Stop
// is called. Start itself does not block. A tick that hits
// ErrHeadroomUnavailable publishes nothing for that tick (gauges retain
// their last good value) rather than writing a misleading zero.
func (p *HeadroomPublisher) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	p.cancelMu.Lock()
	p.cancel = cancel
	p.cancelMu.Unlock()
	go p.run(runCtx)
}

// Stop cancels the publisher's tick loop. Safe to call more than once,
// and safe before Start (context.CancelFunc is itself idempotent).
func (p *HeadroomPublisher) Stop() {
	p.cancelMu.Lock()
	cancel := p.cancel
	p.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// run is the publisher's single background goroutine body.
func (p *HeadroomPublisher) run(ctx context.Context) {
	defer p.ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.ticker.C():
			p.tick()
		}
	}
}

// tick recomputes Headroom and, on success, republishes it to the
// registered gauges (a no-op set if reg was nil at construction).
func (p *HeadroomPublisher) tick() {
	h, err := p.Compute()
	if err == nil {
		p.publish(h)
	}
	if p.afterTick != nil {
		p.afterTick()
	}
}

// publish writes h into this publisher's registered gauges, if any were
// registered (reg != nil at construction).
func (p *HeadroomPublisher) publish(h Headroom) {
	if p.gauges.admitted == nil {
		return
	}
	p.gauges.admitted.Set(int64(h.Admitted))
	p.gauges.ceiling.Set(int64(h.EnforcedCeiling))
	p.gauges.ratioMilli.Set(int64(h.Ratio * 1000))
	p.gauges.censusCount.Set(int64(h.CensusCount))
}

// Compile-time interface satisfaction assertion.
var _ HeadroomModel = (*HeadroomPublisher)(nil)
