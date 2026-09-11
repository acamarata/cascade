package governor

// Purpose: table tests over the exported request/permit/config shapes
//
//	(admission_types.go), plus a controller-free unit test of the
//	priority-queue ordering the container/heap.Interface implementation
//	provides (TestAdmissionPriorityQueueOrder), so admission_test.go
//	does not need to carry it.
import (
	"container/heap"
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// newTestSampler and newTestController below are shared by every _test.go
// file in this package (admission_test.go, admission_errors_test.go),
// hence their home in this file rather than admission_test.go, which is
// already at the 300-line cap.

func TestAdmissionRequestPriorityDefault(t *testing.T) {
	if PriorityDefault != 50 {
		t.Fatalf("PriorityDefault = %d, want 50 (R-21.2)", PriorityDefault)
	}
	if requestPriority(AdmissionRequest{}) != PriorityDefault {
		t.Fatalf("requestPriority of a zero-value request should default to PriorityDefault")
	}
	if got := requestPriority(AdmissionRequest{Priority: 90}); got != 90 {
		t.Fatalf("requestPriority(90) = %d, want 90", got)
	}
}

// TestEnforcedCeilingReflectsCurrentStage proves binding-ceiling honesty
// at the AdmissionController level: EnforcedCeiling tracks whatever
// StageProvider reports right now, never cfg.MaxInflight alone -
// StageCritical halves it, and StageHalt reports exactly the work already
// admitted (zero additional room), never a higher static number.
func TestEnforcedCeilingReflectsCurrentStage(t *testing.T) {
	ac := newTestController(AdmissionConfig{MaxInflight: 8}, ResourceSnapshot{})

	ac.SetStageProvider(func() ThrottleStage { return StageNormal })
	if got := ac.EnforcedCeiling(); got != 8 {
		t.Fatalf("EnforcedCeiling at StageNormal = %d, want 8 (cfg.MaxInflight unchanged)", got)
	}

	ac.SetStageProvider(func() ThrottleStage { return StageCritical })
	if got := ac.EnforcedCeiling(); got != 4 {
		t.Fatalf("EnforcedCeiling at StageCritical = %d, want 4 (halved)", got)
	}

	// At StageHalt with nothing admitted, the honest ceiling is zero
	// additional room - never cfg.MaxInflight=8, which would imply
	// capacity Admit would actually refuse.
	ac.SetStageProvider(func() ThrottleStage { return StageHalt })
	if got := ac.EnforcedCeiling(); got != 0 {
		t.Fatalf("EnforcedCeiling at StageHalt with 0 inflight = %d, want 0 (Inflight(), not cfg.MaxInflight)", got)
	}
}

// TestEnforcedCeilingAtHaltReflectsInflight proves the StageHalt case is
// not simply always zero: it tracks the real in-flight count, so a
// consumer denominating against it sees exactly the room Admit would
// grant (none) rather than a fixed placeholder.
func TestEnforcedCeilingAtHaltReflectsInflight(t *testing.T) {
	ac := newTestController(AdmissionConfig{MaxInflight: 8}, ResourceSnapshot{})
	ac.SetStageProvider(func() ThrottleStage { return StageNormal })

	permit, err := ac.Admit(context.Background(), AdmissionRequest{Weight: 3})
	if err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	defer permit.Release()

	ac.SetStageProvider(func() ThrottleStage { return StageHalt })
	if got := ac.EnforcedCeiling(); got != 3 {
		t.Fatalf("EnforcedCeiling at StageHalt with 3 inflight = %d, want 3", got)
	}
}

func TestRequestWeightNormalization(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{{0, 1}, {-3, 1}, {1, 1}, {5, 5}}
	for _, tc := range cases {
		if got := requestWeight(AdmissionRequest{Weight: tc.in}); got != tc.want {
			t.Fatalf("requestWeight(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestSwapFractionZeroTotal(t *testing.T) {
	if got := swapFraction(ResourceSnapshot{}); got != 0 {
		t.Fatalf("swapFraction of a zero-total snapshot = %v, want 0 (never NaN)", got)
	}
	snap := ResourceSnapshot{SwapUsedBytes: 5, SwapTotalBytes: 10}
	if got := swapFraction(snap); got != 0.5 {
		t.Fatalf("swapFraction = %v, want 0.5", got)
	}
}

// TestPermitZeroValueReleaseIsNoop asserts an unadmitted (zero-value)
// Permit's Release is a safe no-op, so a call site that always defers
// Release() never needs a nil check.
func TestPermitZeroValueReleaseIsNoop(_ *testing.T) {
	var p Permit
	p.Release()
	p.Release() // twice, still a no-op
}

// TestAdmissionPriorityQueueOrder asserts the queue dequeues (Priority
// desc, enqueue-sequence asc) - not FIFO (R-21.215) - exercising
// admissionQueue directly via container/heap, independent of
// AdmissionController.
func TestAdmissionPriorityQueueOrder(t *testing.T) {
	q := &admissionQueue{}
	heap.Init(q)

	type entry struct {
		priority int
		seq      int64
	}
	in := []entry{
		{priority: 10, seq: 1},
		{priority: 90, seq: 2},
		{priority: 90, seq: 3},
		{priority: 50, seq: 4},
	}
	for _, e := range in {
		heap.Push(q, &admissionWaiter{req: AdmissionRequest{Priority: e.priority}, seq: e.seq})
	}

	wantOrder := []entry{
		{priority: 90, seq: 2}, // higher priority first
		{priority: 90, seq: 3}, // same priority, earlier seq first
		{priority: 50, seq: 4},
		{priority: 10, seq: 1}, // lowest priority last
	}
	for i, want := range wantOrder {
		w := heap.Pop(q).(*admissionWaiter)
		if w.req.Priority != want.priority || w.seq != want.seq {
			t.Fatalf("pop %d = (priority=%d seq=%d), want (priority=%d seq=%d)",
				i, w.req.Priority, w.seq, want.priority, want.seq)
		}
	}
	if q.Len() != 0 {
		t.Fatalf("queue not empty after draining: Len() = %d", q.Len())
	}
}

// newTestSampler returns a *Sampler whose Snapshot() is fixed at snap,
// populated by calling tick() directly (no goroutine, no ticker).
func newTestSampler(snap ResourceSnapshot) *Sampler {
	s := &Sampler{
		clk:     runtime.NewFixedClock(time.Unix(0, 0)),
		collect: func() (ResourceSnapshot, error) { return snap, nil },
	}
	s.tick()
	return s
}

func newTestController(cfg AdmissionConfig, snap ResourceSnapshot) *AdmissionController {
	return NewAdmissionController(newTestSampler(snap), cfg, runtime.NewFixedClock(time.Unix(0, 0)))
}

// enqueueAndWait launches req on ac in a goroutine and blocks until it is
// actually queued (via afterEnqueue), returning a channel that delivers
// the eventual (Permit, error).
func enqueueAndWait(ctx context.Context, t *testing.T, ac *AdmissionController, req AdmissionRequest) <-chan admissionResult {
	t.Helper()
	ready := make(chan struct{})
	ac.mu.Lock()
	ac.afterEnqueue = func() { close(ready) }
	ac.mu.Unlock()
	out := make(chan admissionResult, 1)
	go func() {
		p, err := ac.Admit(ctx, req)
		out <- admissionResult{permit: p, err: err}
	}()

	// A bare `<-ready` here is a hang by construction, and it hung: this
	// helper assumes Admit always reaches the queue, but Admit RESOLVES
	// EARLY whenever it grants immediately or refuses fail-closed, in
	// which case afterEnqueue never fires and nothing ever closes ready.
	// The package then burned its whole 10-minute timeout and reported a
	// hang instead of the one-line reason, taking every other governor
	// test's result down with it. This is the same defect, in the same
	// shape, as the one already fixed in internal/secrets' single-flight
	// helper; both are bounded now.
	//
	// out is buffered, so the goroutine never blocks and the result can be
	// put back for a caller that wants to inspect it.
	select {
	case <-ready:
	case res := <-out:
		out <- res
		t.Fatalf("Admit resolved without ever reaching the queue, so this helper's premise does not hold: permit=%v err=%v", res.permit, res.err)
	case <-time.After(enqueueWaitTimeout):
		t.Fatalf("the request neither reached the queue nor resolved within %s", enqueueWaitTimeout)
	}
	return out
}

// enqueueWaitTimeout bounds the wait above. It measures nothing and
// nothing asserts on it; it exists so a stall is reported in seconds
// rather than swallowed by the package timeout.
const enqueueWaitTimeout = 30 * time.Second
