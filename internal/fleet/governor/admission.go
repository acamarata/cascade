// Package governor (admission.go) implements the AdmissionController: the
// admit/queue/drain gate Conductor's fan-out (K/S-23.T2) calls before
// dispatching any concurrently-executing work item (P1-E13-W3-S26-T2).
// Enforces the never-kill invariant, the bounded priority queue
// (Priority desc, enqueue-seq asc - R-21.215 strikes FIFO), and the
// compile-class ceiling via CompileLockRegistry, all against a *Sampler
// snapshot read fresh per decision and an injected runtime.Clock (Art.7.3
// - no bare time.Now).
//
// FAIL CLOSED: a nil Sampler refuses every Admit rather than admitting
// blind. A zero-total snapshot (Windows tier-2) is a DIFFERENT, documented
// case: it contributes a 0 swap fraction (conservative allow on that axis
// only), never a refusal, because it is a known platform limitation, not
// a missing signal. Lock order: admission -> compile lock -> lease
// (R-21.215) - Admit never blocks holding the compile-lock registry's
// mutex; Release never blocks.
//
// SPORT: internal/fleet/governor.AdmissionController (ADD, T-2).
package governor

import (
	"container/heap"
	"context"
	"math"
	"sync"
	"sync/atomic"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// AdmissionController gates concurrent work admission against a shared
// sampler, a bounded priority queue, and a per-repo compile-class ceiling.
// The zero value is not usable; construct with NewAdmissionController.
type AdmissionController struct {
	cfg              AdmissionConfig
	sampler          *Sampler
	clk              runtime.Clock
	registry         *CompileLockRegistry
	stageProviderVal atomic.Value // holds stageProviderBox

	mu           sync.Mutex
	inflight     int
	draining     bool
	queue        admissionQueue
	nextSeq      int64
	drainWaiters []chan struct{}
	// afterEnqueue is a test-only hook (nil in production), invoked
	// under ac.mu right after a waiter is queued, giving tests a
	// non-sleep signal (Art.7.3; mirrors sampler.go's afterTick).
	afterEnqueue func()
}

// NewAdmissionController builds an AdmissionController reading resource
// state from sampler. A zero-value cfg field takes its Default* constant;
// a nil clk falls back to runtime.NewSystemClock() (tests inject
// FixedClock, Art.7.3). sampler may be nil only to exercise the
// fail-closed path; production always supplies a running Sampler.
func NewAdmissionController(sampler *Sampler, cfg AdmissionConfig, clk runtime.Clock) *AdmissionController {
	if cfg.QueueCap <= 0 {
		cfg.QueueCap = DefaultQueueCap
	}
	if cfg.MaxInflight <= 0 {
		cfg.MaxInflight = DefaultMaxInflight
	}
	if cfg.CompileClassCap <= 0 {
		cfg.CompileClassCap = DefaultCompileClassCap
	}
	if cfg.SwapThreshold <= 0 {
		cfg.SwapThreshold = DefaultSwapThreshold
	}
	if clk == nil {
		clk = runtime.NewSystemClock()
	}
	return &AdmissionController{
		cfg:      cfg,
		sampler:  sampler,
		clk:      clk,
		registry: NewCompileLockRegistry(),
	}
}

// Admit requests admission for req: under headroom it returns a Permit
// immediately, over headroom it blocks until dequeued or ctx is
// cancelled. Fails closed (nothing queued) when there is no Sampler to
// read - admitting blind is worse than refusing.
func (ac *AdmissionController) Admit(ctx context.Context, req AdmissionRequest) (Permit, error) {
	if ac.sampler == nil {
		return Permit{}, cascade.New(cascade.KindUnavailable,
			"governor: admission controller has no resource sampler; refusing to admit blind")
	}
	stage := ac.stage()
	if stage == StageHalt {
		return Permit{}, ErrThrottled
	}
	req.Priority = requestPriority(req)
	effMax := ac.effectiveMaxInflight(stage)
	swapFrac := swapFraction(ac.sampler.Snapshot())
	ac.mu.Lock()
	if ac.draining {
		ac.mu.Unlock()
		return Permit{}, ErrDraining
	}
	if ac.canAdmitLocked(req, effMax, swapFrac) {
		permit := ac.grantLocked(req)
		ac.mu.Unlock()
		return permit, nil
	}
	w, err := ac.enqueueLocked(req)
	ac.mu.Unlock()
	if err != nil {
		return Permit{}, err
	}
	return ac.await(ctx, w)
}

// effectiveMaxInflight halves cfg.MaxInflight (floor 1) at StageCritical;
// every other stage leaves it unchanged.
func (ac *AdmissionController) effectiveMaxInflight(stage ThrottleStage) int {
	switch stage {
	case StageCritical:
		eff := ac.cfg.MaxInflight / 2
		if eff < 1 {
			eff = 1
		}
		return eff
	case StageNormal, StageWarn, StageHalt:
		return ac.cfg.MaxInflight
	}
	return ac.cfg.MaxInflight
}

// canAdmitLocked reports whether req fits effMax, the swap threshold, and
// (if CompileLock) the compile-class ceiling. Callers must hold ac.mu.
func (ac *AdmissionController) canAdmitLocked(req AdmissionRequest, effMax int, swapFrac float64) bool {
	if ac.inflight+requestWeight(req) > effMax {
		return false
	}
	if swapFrac > ac.cfg.SwapThreshold {
		return false
	}
	if req.CompileLock && ac.registry.Count(ac.cfg.RepoPath) >= ac.cfg.CompileClassCap {
		return false
	}
	return true
}

// grantLocked commits an admission: bumps in-flight, takes the
// compile-class lock if requested, and returns the Permit. Callers must
// hold ac.mu; Release never blocks (R-21.215).
func (ac *AdmissionController) grantLocked(req AdmissionRequest) Permit {
	weight := requestWeight(req)
	ac.inflight += weight
	var compileUnlock func()
	if req.CompileLock {
		compileUnlock = ac.registry.Lock(ac.cfg.RepoPath)
	}
	state := &permitState{admittedAt: ac.clk.Now()}
	state.release = func() {
		if compileUnlock != nil {
			compileUnlock()
		}
		ac.release(weight)
	}
	return Permit{state: state}
}

// enqueueLocked queues req or reports ErrQueueFull. Callers hold ac.mu.
func (ac *AdmissionController) enqueueLocked(req AdmissionRequest) (*admissionWaiter, error) {
	if ac.queue.Len() >= ac.cfg.QueueCap {
		return nil, ErrQueueFull
	}
	ac.nextSeq++
	w := &admissionWaiter{req: req, seq: ac.nextSeq, resultCh: make(chan admissionResult, 1)}
	heap.Push(&ac.queue, w)
	if ac.afterEnqueue != nil {
		ac.afterEnqueue()
	}
	return w, nil
}

// await blocks on w's result or ctx cancellation; never holds ac.mu.
func (ac *AdmissionController) await(ctx context.Context, w *admissionWaiter) (Permit, error) {
	select {
	case res := <-w.resultCh:
		return res.permit, res.err
	case <-ctx.Done():
		return ac.cancelWaiter(ctx, w)
	}
}

// cancelWaiter handles cancellation racing a concurrent grant or Drain: a
// still-queued w is removed outright; an already-resolved w has its
// result drained and any granted Permit released, so a cancelled caller
// never leaks an admitted slot.
func (ac *AdmissionController) cancelWaiter(ctx context.Context, w *admissionWaiter) (Permit, error) {
	ac.mu.Lock()
	stillQueued := w.index != -1
	if stillQueued {
		heap.Remove(&ac.queue, w.index)
	}
	ac.mu.Unlock()
	wrapped := cascade.Wrap(cascade.KindCanceled, ctx.Err(), "governor: admission canceled")
	if stillQueued {
		return Permit{}, wrapped
	}
	res := <-w.resultCh
	if res.err == nil {
		res.permit.Release()
	}
	return Permit{}, wrapped
}

// release undoes grantLocked: decrements in-flight, drains the queue as
// headroom allows, and wakes Drain on the last in-flight item. Never
// blocks (R-21.215).
func (ac *AdmissionController) release(weight int) {
	ac.mu.Lock()
	ac.inflight -= weight
	ac.drainQueueLocked()
	var waiters []chan struct{}
	if ac.draining && ac.inflight == 0 {
		waiters = ac.drainWaiters
		ac.drainWaiters = nil
	}
	ac.mu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

// drainQueueLocked admits waiters in strict priority order while stage
// and headroom allow it, stopping at the first head item that does not
// fit (skipping it would break ordering). Callers hold ac.mu.
func (ac *AdmissionController) drainQueueLocked() {
	for ac.queue.Len() > 0 {
		stage := ac.stage()
		if stage == StageHalt {
			return
		}
		head := ac.queue[0]
		snap := ResourceSnapshot{}
		if ac.sampler != nil {
			snap = ac.sampler.Snapshot()
		}
		if !ac.canAdmitLocked(head.req, ac.effectiveMaxInflight(stage), swapFraction(snap)) {
			return
		}
		heap.Pop(&ac.queue)
		head.resultCh <- admissionResult{permit: ac.grantLocked(head.req)}
	}
}

// Drain closes the gate (later Admit calls return ErrDraining), fails
// every queued waiter with ErrDraining immediately rather than waiting
// for it to be admitted, then blocks only for in-flight Permits or ctx.
func (ac *AdmissionController) Drain(ctx context.Context) error {
	ac.mu.Lock()
	ac.draining = true
	for ac.queue.Len() > 0 {
		w := heap.Pop(&ac.queue).(*admissionWaiter)
		w.resultCh <- admissionResult{err: ErrDraining}
	}
	if ac.inflight == 0 {
		ac.mu.Unlock()
		return nil
	}
	ch := make(chan struct{})
	ac.drainWaiters = append(ac.drainWaiters, ch)
	ac.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return cascade.Wrap(cascade.KindCanceled, ctx.Err(),
			"governor: drain canceled before in-flight work finished")
	}
}

// Pressure returns max(inflight/MaxInflight, queueDepth/QueueCap,
// swapUsedFraction), the metric S-26.T3's ladder consumes. A zero-total
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
