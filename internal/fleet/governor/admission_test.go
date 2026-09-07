package governor

// Purpose: AdmissionController's behavioral test suite. Every test drives
//
//	a real *Sampler (via a directly-populated snapshot, never a running
//	goroutine) and a FixedClock, and synchronizes on blocked Admit calls
//	via the afterEnqueue test hook rather than a real sleep (Art.7.3).
//
// Constraints: run with -race; the concurrency tests
//
//	(TestAdmissionControllerNeverKills, cancellation-during-queue) are
//	the ones that matter for proving no leaked slot.
import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestAdmissionControllerAdmitImmediate(t *testing.T) {
	ac := newTestController(AdmissionConfig{MaxInflight: 2, QueueCap: 4}, ResourceSnapshot{})
	permit, err := ac.Admit(context.Background(), AdmissionRequest{Kind: "generic"})
	if err != nil {
		t.Fatalf("Admit under headroom returned %v, want nil", err)
	}
	if ac.Inflight() != 1 || ac.QueueDepth() != 0 {
		t.Fatalf("Inflight=%d QueueDepth=%d, want 1,0", ac.Inflight(), ac.QueueDepth())
	}
	permit.Release()
	if ac.Inflight() != 0 {
		t.Fatalf("Inflight after Release = %d, want 0", ac.Inflight())
	}
}

func TestAdmissionControllerQueue(t *testing.T) {
	ac := newTestController(AdmissionConfig{MaxInflight: 1, QueueCap: 4}, ResourceSnapshot{})
	permit1, err := ac.Admit(context.Background(), AdmissionRequest{})
	if err != nil {
		t.Fatalf("first Admit: %v", err)
	}
	res := enqueueAndWait(context.Background(), t, ac, AdmissionRequest{})
	if ac.QueueDepth() != 1 {
		t.Fatalf("QueueDepth while over MaxInflight = %d, want 1", ac.QueueDepth())
	}
	permit1.Release()
	got := <-res
	if got.err != nil {
		t.Fatalf("queued Admit after Release: %v", got.err)
	}
	if ac.QueueDepth() != 0 || ac.Inflight() != 1 {
		t.Fatalf("after drain Inflight=%d QueueDepth=%d, want 1,0", ac.Inflight(), ac.QueueDepth())
	}
	got.permit.Release()
}

func TestAdmissionControllerDrain(t *testing.T) {
	ac := newTestController(AdmissionConfig{MaxInflight: 1, QueueCap: 4}, ResourceSnapshot{})
	permit1, err := ac.Admit(context.Background(), AdmissionRequest{})
	if err != nil {
		t.Fatalf("first Admit: %v", err)
	}
	res := enqueueAndWait(context.Background(), t, ac, AdmissionRequest{})

	drainDone := make(chan error, 1)
	go func() { drainDone <- ac.Drain(context.Background()) }()

	got := <-res
	if !errors.Is(got.err, ErrDraining) {
		t.Fatalf("queued waiter during Drain got %v, want ErrDraining", got.err)
	}
	if _, err := ac.Admit(context.Background(), AdmissionRequest{}); !errors.Is(err, ErrDraining) {
		t.Fatalf("Admit after Drain started = %v, want ErrDraining", err)
	}

	permit1.Release()
	if err := <-drainDone; err != nil {
		t.Fatalf("Drain returned %v, want nil once in-flight work finished", err)
	}
}

func TestDrainReleasesQueuedWaiters(t *testing.T) {
	ac := newTestController(AdmissionConfig{MaxInflight: 1, QueueCap: 4}, ResourceSnapshot{})
	permit1, _ := ac.Admit(context.Background(), AdmissionRequest{})
	res1 := enqueueAndWait(context.Background(), t, ac, AdmissionRequest{})
	res2 := enqueueAndWait(context.Background(), t, ac, AdmissionRequest{Priority: 10})
	if ac.QueueDepth() != 2 {
		t.Fatalf("QueueDepth = %d, want 2 queued waiters", ac.QueueDepth())
	}

	drainDone := make(chan error, 1)
	go func() { drainDone <- ac.Drain(context.Background()) }()

	if got := <-res1; !errors.Is(got.err, ErrDraining) {
		t.Fatalf("waiter 1 got %v, want ErrDraining", got.err)
	}
	if got := <-res2; !errors.Is(got.err, ErrDraining) {
		t.Fatalf("waiter 2 got %v, want ErrDraining", got.err)
	}
	if ac.QueueDepth() != 0 {
		t.Fatalf("QueueDepth after Drain rejects queued waiters = %d, want 0", ac.QueueDepth())
	}
	permit1.Release()
	if err := <-drainDone; err != nil {
		t.Fatalf("Drain = %v, want nil", err)
	}
}

func TestAdmissionControllerNeverKills(t *testing.T) {
	ac := newTestController(AdmissionConfig{MaxInflight: 1, QueueCap: 4}, ResourceSnapshot{})
	permit1, err := ac.Admit(context.Background(), AdmissionRequest{})
	if err != nil {
		t.Fatalf("first Admit: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	res := enqueueAndWait(ctx, t, ac, AdmissionRequest{})
	if ac.Inflight() != 1 {
		t.Fatalf("Inflight while a second request queues = %d, want 1 (permit1 must be untouched)", ac.Inflight())
	}
	cancel()
	if got := <-res; !errors.Is(got.err, context.Canceled) {
		t.Fatalf("cancelled queued Admit = %v, want context.Canceled", got.err)
	}
	// permit1 must still be exactly as valid as before the cancellation:
	// nothing in the controller may have terminated or revoked it.
	if ac.Inflight() != 1 {
		t.Fatalf("Inflight after cancelling the queued waiter = %d, want 1 (permit1 unaffected)", ac.Inflight())
	}
	permit1.Release()
	if ac.Inflight() != 0 {
		t.Fatalf("Inflight after releasing permit1 = %d, want 0", ac.Inflight())
	}
}

func TestAdmissionWindowsFallback(t *testing.T) {
	// A zero-total snapshot is exactly what Sampler.Snapshot() reports on
	// an unsupported platform (tick() never stores on collection error).
	ac := newTestController(AdmissionConfig{MaxInflight: 1, QueueCap: 4, SwapThreshold: 0.01}, ResourceSnapshot{})
	permit, err := ac.Admit(context.Background(), AdmissionRequest{})
	if err != nil {
		t.Fatalf("Admit against a zero-total snapshot = %v, want nil (conservative allow on the swap axis)", err)
	}
	defer permit.Release()

	// The swap axis is not gating, but MaxInflight still is: a second
	// request must still queue rather than being admitted unconditionally.
	res := enqueueAndWait(context.Background(), t, ac, AdmissionRequest{})
	if ac.QueueDepth() != 1 {
		t.Fatal("a second request over MaxInflight was admitted despite the zero-total snapshot: fallback is not unconditional allow")
	}
	permit.Release()
	got := <-res
	if got.err == nil {
		got.permit.Release()
	}
}

func TestAdmissionNilSamplerFailsClosed(t *testing.T) {
	ac := NewAdmissionController(nil, AdmissionConfig{MaxInflight: 4}, runtime.NewFixedClock(time.Unix(0, 0)))
	_, err := ac.Admit(context.Background(), AdmissionRequest{})
	if err == nil {
		t.Fatal("Admit with no Sampler must refuse, not admit blind")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Fatalf("nil-sampler refusal kind = %v (ok=%v), want KindUnavailable", kind, ok)
	}
	if ac.QueueDepth() != 0 {
		t.Fatalf("nil-sampler refusal must not queue anything, QueueDepth = %d", ac.QueueDepth())
	}
}

func TestAdmissionCompileCeilingBinds(t *testing.T) {
	cfg := AdmissionConfig{MaxInflight: 5, QueueCap: 4, CompileClassCap: 1, RepoPath: "/tmp/repo"}
	ac := newTestController(cfg, ResourceSnapshot{})
	req := AdmissionRequest{CompileLock: true}

	permit1, err := ac.Admit(context.Background(), req)
	if err != nil {
		t.Fatalf("first CompileLock Admit: %v", err)
	}
	if got := ac.registry.Count(cfg.RepoPath); got != 1 {
		t.Fatalf("registry.Count after one CompileLock grant = %d, want 1", got)
	}

	res := enqueueAndWait(context.Background(), t, ac, req)
	if ac.QueueDepth() != 1 {
		t.Fatal("a second CompileLock request over CompileClassCap must queue")
	}

	permit1.Release()
	got := <-res
	if got.err != nil {
		t.Fatalf("second CompileLock Admit after Release: %v", got.err)
	}
	if c := ac.registry.Count(cfg.RepoPath); c != 1 {
		t.Fatalf("registry.Count after handoff = %d, want 1", c)
	}
	got.permit.Release()
	if c := ac.registry.Count(cfg.RepoPath); c != 0 {
		t.Fatalf("registry.Count after final release = %d, want 0", c)
	}
}

func TestPermitReleaseUnlocksCompileOnce(t *testing.T) {
	cfg := AdmissionConfig{MaxInflight: 5, QueueCap: 1, CompileClassCap: 1, RepoPath: "/tmp/repo2"}
	ac := newTestController(cfg, ResourceSnapshot{})
	permit, err := ac.Admit(context.Background(), AdmissionRequest{CompileLock: true})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if got := ac.registry.Count(cfg.RepoPath); got != 1 {
		t.Fatalf("registry.Count = %d, want 1", got)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() { defer wg.Done(); permit.Release() }()
	}
	wg.Wait()

	if got := ac.registry.Count(cfg.RepoPath); got != 0 {
		t.Fatalf("registry.Count after double Release = %d, want exactly 0 (unlock must run once)", got)
	}
	if got := ac.Inflight(); got != 0 {
		t.Fatalf("Inflight after double Release = %d, want 0 (must not double-decrement)", got)
	}
}

func TestAdmissionRespectsThrottleStage(t *testing.T) {
	ac := newTestController(AdmissionConfig{MaxInflight: 4, QueueCap: 4}, ResourceSnapshot{})

	ac.SetStageProvider(func() ThrottleStage { return StageHalt })
	if _, err := ac.Admit(context.Background(), AdmissionRequest{}); !errors.Is(err, ErrThrottled) {
		t.Fatalf("Admit at StageHalt = %v, want ErrThrottled", err)
	}
	if ac.QueueDepth() != 0 {
		t.Fatal("StageHalt must queue nothing")
	}

	ac.SetStageProvider(func() ThrottleStage { return StageCritical })
	permit1, err := ac.Admit(context.Background(), AdmissionRequest{})
	if err != nil {
		t.Fatalf("first Admit at StageCritical (effective MaxInflight=2): %v", err)
	}
	permit2, err := ac.Admit(context.Background(), AdmissionRequest{})
	if err != nil {
		t.Fatalf("second Admit at StageCritical: %v", err)
	}
	res := enqueueAndWait(context.Background(), t, ac, AdmissionRequest{})
	if ac.QueueDepth() != 1 {
		t.Fatal("a third request must queue once the halved effective MaxInflight (2) is full")
	}
	permit1.Release()
	got := <-res
	if got.err != nil {
		t.Fatalf("third Admit after release: %v", got.err)
	}
	permit2.Release()
	got.permit.Release()
}

func TestAdmissionPressure(t *testing.T) {
	ac := newTestController(AdmissionConfig{MaxInflight: 2, QueueCap: 2}, ResourceSnapshot{})
	if p := ac.Pressure(); p != 0 {
		t.Fatalf("Pressure of an idle controller = %v, want 0", p)
	}
	permit1, _ := ac.Admit(context.Background(), AdmissionRequest{})
	permit2, _ := ac.Admit(context.Background(), AdmissionRequest{})
	res := enqueueAndWait(context.Background(), t, ac, AdmissionRequest{})

	if p := ac.Pressure(); p != 1.0 {
		t.Fatalf("Pressure at full inflight and one queued = %v, want 1.0 (inflight ratio dominates)", p)
	}
	permit1.Release()
	got := <-res
	if got.err == nil {
		got.permit.Release()
	}
	permit2.Release()
}
