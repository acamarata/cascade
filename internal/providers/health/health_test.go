package health

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
)

// --- TestHealthStateMachine: every valid transition, table-driven golden. ---

func TestHealthStateMachine(t *testing.T) {
	type step struct {
		reason provider.DemotionReason
		want   registry.HealthStatus
	}
	clk := testkit.NewFrozenClock(time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC))
	mgr, reg, _ := newTestManager(t, clk, 3)
	seedProvider(t, reg, "p1")
	ctx := context.Background()

	steps := []step{
		{provider.ReasonRateLimited429, registry.HealthDegraded}, // unknown -> degraded (count=1)
		{provider.ReasonRateLimited429, registry.HealthDegraded}, // degraded -> degraded (count=2)
		{provider.ReasonRateLimited429, registry.HealthDead},     // degraded -> dead (count=3, threshold)
	}
	var got []map[string]any
	for _, s := range steps {
		if err := mgr.DemoteProvider(ctx, "p1", s.reason); err != nil {
			t.Fatalf("DemoteProvider(%s): %v", s.reason, err)
		}
		rec, err := reg.GetProvider(ctx, "p1")
		if err != nil {
			t.Fatalf("GetProvider: %v", err)
		}
		if rec.HealthStatus != s.want {
			t.Fatalf("after %s: health_status=%s want %s", s.reason, rec.HealthStatus, s.want)
		}
		got = append(got, map[string]any{"reason": string(s.reason), "health_status": string(rec.HealthStatus), "demotion_count": rec.DemotionCount})
	}

	// Recovery: dead -> healthy on a manual reset (mirrors RecoverProbe's
	// write; probe_test.go exercises RecoverProbe itself end to end).
	if _, err := reg.AtomicHealthUpdate(ctx, "p1", func(rec registry.ProviderRecord) (registry.ProviderRecord, error) {
		rec.HealthStatus = registry.HealthHealthy
		rec.DemotionCount = 0
		return rec, nil
	}); err != nil {
		t.Fatalf("recovery reset: %v", err)
	}
	rec, _ := reg.GetProvider(ctx, "p1")
	if rec.HealthStatus != registry.HealthHealthy || rec.DemotionCount != 0 {
		t.Fatalf("recovery reset: got status=%s count=%d", rec.HealthStatus, rec.DemotionCount)
	}

	assertGolden(t, "testdata/state_machine.golden.json", got)
}

func TestGetHealthReturnsCurrentStatus(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr, reg, _ := newTestManager(t, clk, 3)
	seedProvider(t, reg, "p1")
	ctx := context.Background()

	got, err := mgr.GetHealth(ctx, "p1")
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if got != provider.HealthUnknown {
		t.Fatalf("GetHealth on a fresh provider = %q, want unknown", got)
	}
	if err := mgr.DemoteProvider(ctx, "p1", provider.ReasonRateLimited429); err != nil {
		t.Fatalf("DemoteProvider: %v", err)
	}
	got, err = mgr.GetHealth(ctx, "p1")
	if err != nil {
		t.Fatalf("GetHealth #2: %v", err)
	}
	if got != provider.HealthDegraded {
		t.Fatalf("GetHealth after demotion = %q, want degraded", got)
	}
}

func TestGetHealthUnknownProviderRefused(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr, _, _ := newTestManager(t, clk, 3)
	if _, err := mgr.GetHealth(context.Background(), "ghost"); err == nil {
		t.Fatal("expected ErrProviderNotFound for an unknown provider")
	}
}

func TestNewManagerDefaultsThreshold(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr, reg, _ := newTestManager(t, clk, 0) // 0 -> DefaultEvictionThreshold
	seedProvider(t, reg, "p1")
	ctx := context.Background()
	for i := 0; i < DefaultEvictionThreshold; i++ {
		if err := mgr.DemoteProvider(ctx, "p1", provider.ReasonRateLimited429); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	rec, _ := reg.GetProvider(ctx, "p1")
	if rec.HealthStatus != registry.HealthDead {
		t.Fatalf("default threshold not applied: status=%s count=%d", rec.HealthStatus, rec.DemotionCount)
	}
}

func TestDemoteProviderInvalidReasonRejected(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr, reg, _ := newTestManager(t, clk, 3)
	seedProvider(t, reg, "p1")
	if err := mgr.DemoteProvider(context.Background(), "p1", provider.DemotionReason("bogus")); err == nil {
		t.Fatal("expected an error for an invalid DemotionReason")
	}
}

func TestDemoteProviderEmptyNameRefused(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr, _, _ := newTestManager(t, clk, 3)
	if err := mgr.DemoteProvider(context.Background(), "", provider.ReasonRateLimited429); err == nil {
		t.Fatal("expected an error for an empty provider name")
	}
}

func TestDemoteProviderUnknownNameRefused(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr, _, _ := newTestManager(t, clk, 3)
	if err := mgr.DemoteProvider(context.Background(), "ghost", provider.ReasonRateLimited429); err == nil {
		t.Fatal("expected ErrProviderNotFound for an unknown provider")
	}
}

// --- TestDeadKeyHardBypass: 401/403-no-Retry-After bypasses the threshold. ---

func TestDeadKeyHardBypass(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr, reg, _ := newTestManager(t, clk, 100) // threshold far above 1, to prove the bypass
	seedProvider(t, reg, "p1")
	ctx := context.Background()

	if err := mgr.DemoteProvider(ctx, "p1", provider.ReasonDeadKeyHard); err != nil {
		t.Fatalf("DemoteProvider(dead_key_hard): %v", err)
	}
	rec, err := reg.GetProvider(ctx, "p1")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if rec.HealthStatus != registry.HealthDead {
		t.Fatalf("hard dead-key: health_status=%s want dead (threshold=100, count=%d)", rec.HealthStatus, rec.DemotionCount)
	}
}

// --- TestDemotionThresholdSequence: N soft demotions reach dead at N=threshold. ---

func TestDemotionThresholdSequence(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	const threshold = 3
	mgr, reg, _ := newTestManager(t, clk, threshold)
	seedProvider(t, reg, "p1")
	ctx := context.Background()

	wantSequence := []registry.HealthStatus{registry.HealthDegraded, registry.HealthDegraded, registry.HealthDead}
	for i, want := range wantSequence {
		if err := mgr.DemoteProvider(ctx, "p1", provider.ReasonRateLimited429); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		rec, err := reg.GetProvider(ctx, "p1")
		if err != nil {
			t.Fatalf("GetProvider: %v", err)
		}
		if rec.HealthStatus != want {
			t.Fatalf("call %d: health_status=%s want %s (demotion_count=%d)", i+1, rec.HealthStatus, want, rec.DemotionCount)
		}
		if rec.DemotionCount != i+1 {
			t.Fatalf("call %d: demotion_count=%d want %d", i+1, rec.DemotionCount, i+1)
		}
	}
}

// --- TestRecoveryReset: RecoverProbe on a dead provider resets state. See probe_test.go. ---

// --- TestEventEmission: every health_status flip emits exactly one event. ---

func TestEventEmission(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC))
	mgr, reg, bus := newTestManager(t, clk, 3)
	seedProvider(t, reg, "p1")
	ctx := context.Background()
	ch := subscribeEvents(t, bus, eventNamespace, "test-cursor")

	if err := mgr.DemoteProvider(ctx, "p1", provider.ReasonRateLimited429); err != nil {
		t.Fatalf("DemoteProvider #1: %v", err)
	}
	ev := <-ch
	var payload ChangedPayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.PrevStatus != "" || payload.NewStatus != "degraded" || payload.DemotionCount != 1 {
		t.Fatalf("unexpected payload: %+v", payload)
	}

	// Second call: degraded(count=1) -> degraded(count=2), threshold=3, no
	// status flip -- must publish nothing.
	if err := mgr.DemoteProvider(ctx, "p1", provider.ReasonRateLimited429); err != nil {
		t.Fatalf("DemoteProvider #2: %v", err)
	}
	select {
	case ev2 := <-ch:
		t.Fatalf("unexpected second event for a demotion_count-only bump (no status flip): %+v", ev2)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestEventEmissionCapabilityDeniedIsSilent asserts the capability_denied
// no-op path never publishes an event.
func TestEventEmissionCapabilityDeniedIsSilent(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr, reg, bus := newTestManager(t, clk, 3)
	seedProvider(t, reg, "p1")
	ctx := context.Background()
	ch := subscribeEvents(t, bus, eventNamespace, "cap-cursor")

	if err := mgr.DemoteProvider(ctx, "p1", provider.ReasonCapabilityDenied); err != nil {
		t.Fatalf("DemoteProvider(capability_denied): %v", err)
	}
	select {
	case ev := <-ch:
		t.Fatalf("capability_denied must never publish an event, got %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

// --- Capability-denied path: no demotion_count/pool/health_status change. ---

func TestCapabilityDeniedDoesNotDemote(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr, reg, _ := newTestManager(t, clk, 3)
	seedProvider(t, reg, "p1")
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := mgr.DemoteProvider(ctx, "p1", provider.ReasonCapabilityDenied); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	rec, err := reg.GetProvider(ctx, "p1")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if rec.DemotionCount != 0 || rec.HealthStatus != registry.HealthUnknown {
		t.Fatalf("capability_denied mutated health state: status=%s count=%d", rec.HealthStatus, rec.DemotionCount)
	}
}

// --- Concurrency: >=10 goroutines racing DemoteProvider on one name. ---

func TestConcurrentDemote(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	const threshold = 1000 // stays degraded throughout, so every call is a plain increment
	mgr, reg, _ := newTestManager(t, clk, threshold)
	seedProvider(t, reg, "p1")
	ctx := context.Background()

	const n = 20
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			errCh <- mgr.DemoteProvider(ctx, "p1", provider.ReasonRateLimited429)
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	rec, err := reg.GetProvider(ctx, "p1")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if rec.DemotionCount != n {
		t.Fatalf("demotion_count=%d want %d (no lost updates)", rec.DemotionCount, n)
	}
}
