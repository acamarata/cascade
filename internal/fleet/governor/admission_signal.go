// Package governor (admission_signal.go) classifies the resource signal
// every admission decision reads, so Admit and drainQueueLocked share one
// rule for "is there a fresh reading to decide on at all".
//
// Purpose: SignalPosture and ClassifySignal (the closed posture table),
//
//	the two refusal sentinels, AdmissionController.Posture, the memory and
//	swap headroom checks, and Pressure. An unknown reading is never
//	permission: a stale or absent sample refuses immediately, and a
//	snapshot with no memory total refuses whatever its swap total says,
//	because swap use alone says nothing about memory headroom.
//
// Constraints: no bare time.Now; "now" is always the controller's
//
//	injected Clock and the staleness window is StaleAfterPeriods sampler
//	periods.
package governor

import (
	"container/heap"
	"math"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// SignalPosture names which resource readings an admission decision can
// rely on. The set is closed; ClassifySignal returns exactly one value.
type SignalPosture string

// The four signal postures, in the order ClassifySignal tests for them.
const (
	// PostureSwapAndMemory: a fresh sample with memory and swap totals;
	// both thresholds apply.
	PostureSwapAndMemory SignalPosture = "swap+memory"
	// PostureMemoryOnly: a fresh sample with a memory total and no swap
	// (a swap-less host); only the memory threshold applies.
	PostureMemoryOnly SignalPosture = "memory-only"
	// PostureNoSignal: a fresh sample with no memory total. Admit refuses.
	PostureNoSignal SignalPosture = "no-signal"
	// PostureStale: no sample yet, or the newest one is older than
	// StaleAfterPeriods sampler periods. Admit refuses.
	PostureStale SignalPosture = "stale"
)

// StaleAfterPeriods is how many sampler periods a snapshot stays fresh.
// A sample exactly this old is still fresh; one nanosecond older is stale.
const StaleAfterPeriods = 3

// ErrNoResourceSignal is returned (wrapped, posture=no-signal) by Admit
// when the newest sample carries no memory total. Nothing is queued.
var ErrNoResourceSignal = cascade.New(cascade.KindUnavailable, "governor: no memory signal; refusing to admit blind")

// ErrStaleResourceSignal is returned (wrapped, posture=stale) by Admit
// when no sample exists yet or the newest one is too old, and delivered to
// queued waiters when the signal is lost. Nothing is queued.
var ErrStaleResourceSignal = cascade.New(cascade.KindUnavailable, "governor: resource sample missing or stale; refusing to admit")

// ClassifySignal places snap in the closed posture table, evaluated in
// order: zero SampledAt or older than StaleAfterPeriods*period is stale;
// then a zero memory total is no-signal (whatever the swap total); then a
// zero swap total is memory-only; otherwise swap+memory.
func ClassifySignal(snap ResourceSnapshot, now time.Time, period time.Duration) SignalPosture {
	if snap.SampledAt.IsZero() || now.Sub(snap.SampledAt) > StaleAfterPeriods*period {
		return PostureStale
	}
	if snap.MemTotalBytes == 0 {
		return PostureNoSignal
	}
	if snap.SwapTotalBytes == 0 {
		return PostureMemoryOnly
	}
	return PostureSwapAndMemory
}

// Posture reports the controller's current signal posture and the
// SampledAt of the snapshot it was classified from. A controller with no
// Sampler reports PostureStale with a zero time.
func (ac *AdmissionController) Posture() (SignalPosture, time.Time) {
	snap, now, period := ac.signalInputs()
	return ClassifySignal(snap, now, period), snap.SampledAt
}

// signalInputs reads the three ClassifySignal inputs: the newest snapshot
// (zero without a Sampler), the injected clock's now, and the sampler
// period.
func (ac *AdmissionController) signalInputs() (ResourceSnapshot, time.Time, time.Duration) {
	if ac.sampler == nil {
		return ResourceSnapshot{}, ac.clk.Now(), PeriodForHz(0)
	}
	return ac.sampler.Snapshot(), ac.clk.Now(), ac.sampler.Period()
}

// signalRefusal returns the wrapped refusal for a stale or no-signal
// posture, or nil when the posture carries a usable memory reading.
func signalRefusal(posture SignalPosture, snap ResourceSnapshot, now time.Time) error {
	switch posture {
	case PostureStale:
		age := "never-sampled"
		if !snap.SampledAt.IsZero() {
			age = now.Sub(snap.SampledAt).String()
		}
		return cascade.Wrapf(cascade.KindUnavailable, ErrStaleResourceSignal, "posture=stale age=%s", age)
	case PostureNoSignal:
		return cascade.Wrapf(cascade.KindUnavailable, ErrNoResourceSignal, "posture=no-signal")
	case PostureMemoryOnly, PostureSwapAndMemory:
		return nil
	}
	return cascade.Wrapf(cascade.KindUnavailable, ErrStaleResourceSignal, "posture=%s", posture)
}

// reevaluate is the Sampler tick hook: it re-runs the queue drain against
// the newest snapshot, so waiters are granted when headroom returns and
// refused when the signal goes stale or signal-less, without a new Admit.
func (ac *AdmissionController) reevaluate() {
	ac.mu.Lock()
	ac.drainQueueLocked()
	ac.mu.Unlock()
}

// samplerStopped is the Sampler stop hook. With no further samples the
// controller can never decide again, so it closes the gate like Drain:
// every queued waiter gets ErrDraining and later Admit calls are refused.
func (ac *AdmissionController) samplerStopped() {
	ac.mu.Lock()
	ac.draining = true
	ac.refuseQueueLocked(ErrDraining)
	ac.mu.Unlock()
}

// refuseQueueLocked empties the queue, delivering err to every waiter.
// Callers hold ac.mu.
func (ac *AdmissionController) refuseQueueLocked(err error) {
	for ac.queue.Len() > 0 {
		w := heap.Pop(&ac.queue).(*admissionWaiter)
		w.resultCh <- admissionResult{err: err}
	}
}

// resourceHeadroom reports whether snap leaves room under the thresholds
// its posture makes applicable: memory for memory-only, memory and swap
// for swap+memory. Any other posture has no headroom.
func (ac *AdmissionController) resourceHeadroom(posture SignalPosture, snap ResourceSnapshot) bool {
	switch posture {
	case PostureMemoryOnly:
		return memFraction(snap) <= ac.cfg.MemThreshold
	case PostureSwapAndMemory:
		return memFraction(snap) <= ac.cfg.MemThreshold && swapFraction(snap) <= ac.cfg.SwapThreshold
	case PostureNoSignal, PostureStale:
		return false
	}
	return false
}

// memFraction reports snap's memory-used fraction; a zero total (which
// ClassifySignal already refuses) reads as fully used, never as idle.
func memFraction(snap ResourceSnapshot) float64 {
	if snap.MemTotalBytes == 0 {
		return 1
	}
	return float64(snap.MemUsedBytes) / float64(snap.MemTotalBytes)
}

// swapFraction reports snap's swap-used fraction, 0 (never NaN) for a
// zero swap total. It is only consulted under PostureSwapAndMemory and by
// Pressure; a swap-less host is gated on memory instead.
func swapFraction(snap ResourceSnapshot) float64 {
	if snap.SwapTotalBytes == 0 {
		return 0
	}
	return float64(snap.SwapUsedBytes) / float64(snap.SwapTotalBytes)
}

// Pressure returns max(inflight/MaxInflight, queueDepth/QueueCap,
// swapUsedFraction), the metric the throttle ladder consumes. A zero-total
// (or nil-Sampler) snapshot contributes 0, never NaN.
func (ac *AdmissionController) Pressure() float64 {
	ac.mu.Lock()
	inflight := ac.inflight
	queueDepth := ac.queue.Len()
	ac.mu.Unlock()
	var snap ResourceSnapshot
	if ac.sampler != nil {
		snap = ac.sampler.Snapshot()
	}
	inflightRatio, queueRatio := 0.0, 0.0
	if ac.cfg.MaxInflight > 0 {
		inflightRatio = float64(inflight) / float64(ac.cfg.MaxInflight)
	}
	if ac.cfg.QueueCap > 0 {
		queueRatio = float64(queueDepth) / float64(ac.cfg.QueueCap)
	}
	return math.Max(inflightRatio, math.Max(queueRatio, swapFraction(snap)))
}
