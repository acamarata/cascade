package governor

// Purpose: the admission-signal contract, driven through a real Sampler
//
//	goroutine, fake Ticker and shared FrozenClock (no sleeps). Sentinels
//	share KindUnavailable with ErrDraining, so they are asserted by chain
//	identity plus message, never errors.Is alone.
import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

type signalRig struct {
	clk     *testkit.FrozenClock
	ft      *fakeTicker
	s       *Sampler
	ac      *AdmissionController
	settled chan struct{}
	mu      sync.Mutex
	snap    ResourceSnapshot
	err     error
}

func newSignalRig(t *testing.T, cfg AdmissionConfig) *signalRig {
	t.Helper()
	r := &signalRig{clk: testkit.NewFrozenClock(time.Unix(10_000, 0)), ft: newFakeTicker(), settled: make(chan struct{}, 1)}
	r.s = NewSampler(SamplerConfig{}, r.clk, r.ft, nil)
	r.s.collect = func() (ResourceSnapshot, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.snap, r.err
	}
	r.s.afterTick = func() { r.settled <- struct{}{} }
	r.ac = NewAdmissionController(r.s, cfg, r.clk)
	r.s.Start(context.Background())
	t.Cleanup(r.s.Stop)
	return r
}

func (r *signalRig) set(snap ResourceSnapshot, err error) {
	r.mu.Lock()
	r.snap, r.err = snap, err
	r.mu.Unlock()
}

// step advances the clock by d and runs one full tick, hooks included.
func (r *signalRig) step(t *testing.T, d time.Duration) {
	t.Helper()
	r.clk.Advance(d)
	ctx, cancel := context.WithTimeout(context.Background(), signalWait)
	defer cancel()
	r.ft.Tick(ctx)
	select {
	case <-r.settled:
	case <-ctx.Done():
		t.Fatal("tick did not settle")
	}
}

var signalWait, errCollectorDown = 10 * time.Second, cascade.New(cascade.KindUnavailable, "test collector down")

func memSnap(pct uint64) ResourceSnapshot {
	return ResourceSnapshot{MemTotalBytes: 100 << 20, MemUsedBytes: pct << 20}
}

func bothSnap(memPct, swapPct uint64) ResourceSnapshot {
	s := memSnap(memPct)
	s.SwapTotalBytes, s.SwapUsedBytes = 100<<20, swapPct<<20
	return s
}

// assertSentinel requires sentinel itself in err's chain, its message in
// err's text, and detail (the posture) in err's text.
func assertSentinel(t *testing.T, err error, sentinel *cascade.Error, detail string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want %q (%s)", sentinel.Msg, detail)
	}
	found := false
	for e := err; e != nil; e = errors.Unwrap(e) {
		if e == error(sentinel) {
			found = true
		}
	}
	msg := err.Error()
	if !found || !strings.Contains(msg, sentinel.Msg) || !strings.Contains(msg, detail) {
		t.Fatalf("err = %q, want sentinel %q by identity with %q", msg, sentinel.Msg, detail)
	}
}

func assertEmpty(t *testing.T, ac *AdmissionController) {
	t.Helper()
	if ac.Inflight() != 0 || ac.QueueDepth() != 0 {
		t.Fatalf("Inflight=%d QueueDepth=%d, want 0,0", ac.Inflight(), ac.QueueDepth())
	}
}

// admitOnce calls Admit, cancelling it if it ever queues (so a broken
// refusal fails its assertion instead of blocking), and releases any permit.
func admitOnce(ac *AdmissionController) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ac.mu.Lock()
	ac.afterEnqueue = cancel
	ac.mu.Unlock()
	p, err := ac.Admit(ctx, AdmissionRequest{})
	p.Release()
	return err
}

func TestAdmitRefusesWithoutResourceSignal(t *testing.T) {
	cases := map[string]ResourceSnapshot{
		"no memory, no swap": {},
		"swap only":          {SwapTotalBytes: 8 << 30},
	}
	for name, snap := range cases {
		t.Run(name, func(t *testing.T) {
			r := newSignalRig(t, AdmissionConfig{MaxInflight: 4, QueueCap: 4})
			r.set(snap, nil)
			r.step(t, time.Second)
			assertSentinel(t, admitOnce(r.ac), ErrNoResourceSignal, "posture=no-signal")
			assertEmpty(t, r.ac)
		})
	}
}

func TestAdmitRefusesStaleSnapshot(t *testing.T) {
	r := newSignalRig(t, AdmissionConfig{MaxInflight: 4, QueueCap: 4})
	assertSentinel(t, admitOnce(r.ac), ErrStaleResourceSignal, "posture=stale age=never-sampled")
	assertEmpty(t, r.ac)

	r.set(memSnap(10), nil)
	r.step(t, time.Second)
	if err := admitOnce(r.ac); err != nil {
		t.Fatalf("fresh Admit = %v", err)
	}
	r.set(ResourceSnapshot{}, errCollectorDown)
	for i := 0; i < StaleAfterPeriods; i++ {
		r.step(t, time.Second)
	}
	if err := admitOnce(r.ac); err != nil {
		t.Fatalf("Admit at exactly %d periods old = %v, want admit (boundary is fresh)", StaleAfterPeriods, err)
	}
	r.step(t, time.Second)
	assertSentinel(t, admitOnce(r.ac), ErrStaleResourceSignal, "posture=stale age=4s")
	assertEmpty(t, r.ac)

	r.set(memSnap(10), nil)
	r.step(t, time.Second)
	if err := admitOnce(r.ac); err != nil {
		t.Fatalf("Admit after a fresh tick = %v, want admit", err)
	}
}

// awaitResult reads a queued Admit's outcome; assertStillQueued requires
// it unresolved and queued.
func awaitResult(t *testing.T, res <-chan admissionResult) admissionResult {
	t.Helper()
	select {
	case got := <-res:
		return got
	case <-time.After(signalWait):
		t.Fatal("queued Admit was never resolved")
	}
	return admissionResult{}
}

func assertStillQueued(t *testing.T, ac *AdmissionController, res <-chan admissionResult) {
	t.Helper()
	select {
	case got := <-res:
		t.Fatalf("waiter resolved early: err=%v", got.err)
	default:
	}
	if ac.QueueDepth() != 1 || (ac.cfg.MaxInflight > 1 && ac.Inflight() != 0) {
		t.Fatalf("QueueDepth=%d Inflight=%d, want 1 queued and none granted", ac.QueueDepth(), ac.Inflight())
	}
}

func TestAdmitMemoryOnlyFollowsMemoryHeadroom(t *testing.T) {
	for _, set := range []float64{0, -1} {
		if got := NewAdmissionController(nil, AdmissionConfig{MemThreshold: set}, testkit.NewFrozenClock(time.Unix(0, 0))).cfg.MemThreshold; got != 0.90 || DefaultMemThreshold != 0.90 {
			t.Fatalf("MemThreshold %v resolves to %v (default %v), want 0.90", set, got, DefaultMemThreshold)
		}
	}
	r := newSignalRig(t, AdmissionConfig{MaxInflight: 4, QueueCap: 4})
	r.set(memSnap(90), nil)
	r.step(t, time.Second)
	if got, _ := r.ac.Posture(); got != PostureMemoryOnly {
		t.Fatalf("Posture = %q, want memory-only", got)
	}
	if err := admitOnce(r.ac); err != nil {
		t.Fatalf("Admit at exactly MemThreshold = %v, want admit", err)
	}
	r.set(memSnap(95), nil)
	r.step(t, time.Second)
	res := enqueueAndWait(context.Background(), t, r.ac, AdmissionRequest{})
	assertStillQueued(t, r.ac, res) // and Inflight 0: no permit was granted
	r.set(memSnap(50), nil)
	r.step(t, time.Second)
	got := awaitResult(t, res)
	if got.err != nil || r.ac.Inflight() != 1 {
		t.Fatalf("drained waiter err=%v Inflight=%d, want nil,1", got.err, r.ac.Inflight())
	}
	got.permit.Release()
}

func TestAdmitSwapAndMemoryBothApply(t *testing.T) {
	cases := []struct {
		name      string
		snap      ResourceSnapshot
		wantQueue bool
	}{
		{"swap over threshold", bothSnap(50, 60), true},
		{"memory over threshold", bothSnap(95, 10), true},
		{"both under", bothSnap(50, 40), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newSignalRig(t, AdmissionConfig{MaxInflight: 4, QueueCap: 4})
			r.set(tc.snap, nil)
			r.step(t, time.Second)
			if got, _ := r.ac.Posture(); got != PostureSwapAndMemory {
				t.Fatalf("Posture = %q, want swap+memory", got)
			}
			if !tc.wantQueue {
				if err := admitOnce(r.ac); err != nil {
					t.Fatalf("Admit under both thresholds = %v", err)
				}
				return
			}
			ctx, cancel := context.WithCancel(context.Background())
			res := enqueueAndWait(ctx, t, r.ac, AdmissionRequest{})
			assertStillQueued(t, r.ac, res)
			cancel()
			if got := awaitResult(t, res); !errors.Is(got.err, context.Canceled) {
				t.Fatalf("cancelled waiter = %v", got.err)
			}
		})
	}
}

// TestQueuedWaiterWokenOnSignalLoss: the tick hook alone refuses queued
// waiters on signal loss, and never grants one queued behind MaxInflight.
// Ticks land 1ms late, as real ones do, so StaleAfterPeriods failures
// cross the window.
func TestQueuedWaiterWokenOnSignalLoss(t *testing.T) {
	late := time.Second + time.Millisecond
	cases := []struct {
		name     string
		cfg      AdmissionConfig
		queuedAt ResourceSnapshot
		lostSnap ResourceSnapshot
		lostErr  error
		ticks    int
		want     *cascade.Error
		detail   string
	}{
		{"memory pressure then stale", AdmissionConfig{MaxInflight: 4, QueueCap: 4}, memSnap(95), ResourceSnapshot{}, errCollectorDown, StaleAfterPeriods, ErrStaleResourceSignal, "posture=stale"},
		{"memory pressure then no-signal", AdmissionConfig{MaxInflight: 4, QueueCap: 4}, memSnap(95), ResourceSnapshot{}, nil, 1, ErrNoResourceSignal, "posture=no-signal"},
		{"inflight full then stale", AdmissionConfig{MaxInflight: 1, QueueCap: 4}, memSnap(10), ResourceSnapshot{}, errCollectorDown, StaleAfterPeriods, ErrStaleResourceSignal, "posture=stale"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newSignalRig(t, tc.cfg)
			r.set(tc.queuedAt, nil)
			r.step(t, time.Second)
			var held Permit
			if tc.cfg.MaxInflight == 1 {
				p, err := r.ac.Admit(context.Background(), AdmissionRequest{})
				if err != nil {
					t.Fatalf("first Admit = %v", err)
				}
				held = p
			}
			res := enqueueAndWait(context.Background(), t, r.ac, AdmissionRequest{})
			r.set(tc.lostSnap, tc.lostErr)
			for i := 1; i < tc.ticks; i++ {
				r.step(t, late)
				assertStillQueued(t, r.ac, res)
			}
			r.step(t, late)
			got := awaitResult(t, res)
			if got.err == nil {
				got.permit.Release()
			}
			assertSentinel(t, got.err, tc.want, tc.detail)
			held.Release()
			assertEmpty(t, r.ac)
		})
	}
}
