// Purpose: R-14.117 split of dispatcher_test.go (Art.10.3's 300-line cap
//
//	— behaviour-preserving, moved code only, no rewrites): trigger-miss
//	no-op, NewDispatcher's full required-field validation (error-path
//	coverage per Art.3 DoD), Run's Subscribe-error propagation,
//	zero-registered-hooks no-op, and the blocking-hook timeout/survival
//	proof. Shares dispatcher_test.go's package-level fixtures (fakes,
//	testBus, awaitAuditFire, newTestDispatcher) via the shared package
//	hooks white-box test binary — no duplication.
//
// SPORT: internal.hooks.Dispatcher/ADDED (tests, split 2/2)
//
//	(P1-E03-W1-S05-T1).
package hooks

import (
	"context"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/testkit"
)

func TestHooksAuditEventEmittedOnPanic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bus, clock := testBus(t)

	reg := NewRegistry()
	cfg, err := reg.Register(HookConfig{
		ID:         "note-hook-panic",
		Trigger:    "scheduler.tick",
		ActionType: ActionTypeAgentNote,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	nw := &fakeNoteWriter{panic: true}
	d := newTestDispatcher(t, reg, bus, clock, &fakePluginDispatcher{}, nw, time.Second)

	auditSub, err := bus.Subscribe(ctx, "audit", "test-observer", 8)
	if err != nil {
		t.Fatalf("Subscribe(audit): %v", err)
	}

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	go func() { _ = d.Run(runCtx) }()

	if _, err := bus.Publish(ctx, "triggers", "scheduler.tick", "test", nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	fire := awaitAuditFire(t, ctx, auditSub, cfg.ID)
	if fire.ResultCode != ResultPanic {
		t.Fatalf("ResultCode = %q, want panic", fire.ResultCode)
	}
	if fire.ErrMsg == "" {
		t.Fatal("ErrMsg empty on a panic fire")
	}
}

func TestDispatcher_TriggerMatching_Miss_NoAudit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bus, clock := testBus(t)

	reg := NewRegistry()
	if _, err := reg.Register(HookConfig{ID: "h", Trigger: "wanted", ActionType: ActionTypePluginCall}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	pd := &fakePluginDispatcher{}
	d := newTestDispatcher(t, reg, bus, clock, pd, &fakeNoteWriter{}, time.Second)

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	go func() { _ = d.Run(runCtx) }()

	if _, err := bus.Publish(ctx, "triggers", "unwanted", "test", nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if _, err := bus.Publish(ctx, "triggers", "wanted", "test", nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	auditSub, err := bus.Subscribe(ctx, "audit", "observer2", 8)
	if err != nil {
		t.Fatalf("Subscribe(audit): %v", err)
	}
	awaitAuditFire(t, ctx, auditSub, "h")

	if len(pd.calls) != 1 {
		t.Fatalf("PluginDispatcher calls = %v, want exactly 1 (the miss must not have dispatched)", pd.calls)
	}
}

// TestNewDispatcher_ValidatesEveryRequiredField is the ERROR-PATH coverage
// for DispatcherConfig validation (Art.3 DoD: happy-path-only is a
// blocking CR-A finding) — each required field, omitted alone, must
// refuse construction.
func TestNewDispatcher_ValidatesEveryRequiredField(t *testing.T) {
	bus, clock := testBus(t)
	base := func() DispatcherConfig {
		fw, tok := testFirewall(t)
		return DispatcherConfig{
			Registry:         NewRegistry(),
			Bus:              bus,
			Clock:            clock,
			Egress:           fw,
			EgressToken:      tok,
			PluginDispatcher: &fakePluginDispatcher{},
			NoteWriter:       &fakeNoteWriter{},
			ActionTimeout:    time.Second,
			TriggerNamespace: "triggers",
			AuditNamespace:   "audit",
			CursorName:       "dispatcher",
			SubscribeBuffer:  8,
		}
	}

	cases := []struct {
		name   string
		mutate func(*DispatcherConfig)
	}{
		{"missing Registry", func(c *DispatcherConfig) { c.Registry = nil }},
		{"missing Bus", func(c *DispatcherConfig) { c.Bus = nil }},
		{"missing Clock", func(c *DispatcherConfig) { c.Clock = nil }},
		{"missing Egress", func(c *DispatcherConfig) { c.Egress = nil }},
		{"zero EgressToken", func(c *DispatcherConfig) { c.EgressToken = egress.Capability{} }},
		{"missing PluginDispatcher", func(c *DispatcherConfig) { c.PluginDispatcher = nil }},
		{"missing NoteWriter", func(c *DispatcherConfig) { c.NoteWriter = nil }},
		{"non-positive ActionTimeout", func(c *DispatcherConfig) { c.ActionTimeout = 0 }},
		{"missing TriggerNamespace", func(c *DispatcherConfig) { c.TriggerNamespace = "" }},
		{"missing AuditNamespace", func(c *DispatcherConfig) { c.AuditNamespace = "" }},
		{"missing CursorName", func(c *DispatcherConfig) { c.CursorName = "" }},
		{"non-positive SubscribeBuffer", func(c *DispatcherConfig) { c.SubscribeBuffer = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(&cfg)
			if _, err := NewDispatcher(cfg); err == nil {
				t.Fatalf("NewDispatcher(%s) = nil error, want a validation error", tc.name)
			}
		})
	}

	if _, err := NewDispatcher(base()); err != nil {
		t.Fatalf("NewDispatcher(fully populated) = %v, want nil", err)
	}
}

// TestDispatcher_Run_SubscribeError proves Run propagates a Subscribe
// failure (here: a duplicate active subscription under the same cursor
// name, events.Bus's own documented KindConflict case) rather than
// silently swallowing it.
func TestDispatcher_Run_SubscribeError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	bus, clock := testBus(t)

	// Hold the cursor name Run itself will try to use, so its own
	// Subscribe call collides.
	held, err := bus.Subscribe(ctx, "triggers", "dispatcher", 1)
	if err != nil {
		t.Fatalf("Subscribe (holder): %v", err)
	}
	defer func() { _ = held.Unsubscribe() }()

	reg := NewRegistry()
	d := newTestDispatcher(t, reg, bus, clock, &fakePluginDispatcher{}, &fakeNoteWriter{}, time.Second)

	if err := d.Run(ctx); err == nil {
		t.Fatal("Run returned nil error for a colliding Subscribe, want the propagated error")
	}
}

func TestDispatcher_ZeroHooks_NoOp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	bus, clock := testBus(t)

	reg := NewRegistry()
	pd := &fakePluginDispatcher{}
	nw := &fakeNoteWriter{}
	d := newTestDispatcher(t, reg, bus, clock, pd, nw, time.Second)

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(runCtx) }()

	if _, err := bus.Publish(ctx, "triggers", "anything", "test", nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	runCancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("Run did not exit after cancellation")
	}
	if len(pd.calls) != 0 || len(nw.calls) != 0 {
		t.Fatalf("zero-hook registry dispatched an action: pd=%v nw=%v", pd.calls, nw.calls)
	}
}

// TestDispatcher_BlockingHook_TimesOutAndSurvives is the ticket's central
// requirement: a hook whose action blocks forever, ignoring ctx
// cancellation entirely, must not hang the dispatch engine. ActionTimeout
// here is a short, EXPLICIT duration that IS the feature under test — the
// dispatcher's own bound — not a sleep standing in for synchronisation
// (R-14.136 targets the latter). The test avoids any raw time.Sleep: it
// synchronises on fakePluginDispatcher.blocked (closed the instant the
// fake goroutine enters its permanent block) before asserting anything,
// and otherwise only waits on channel receives bounded by the outer ctx.
func TestDispatcher_BlockingHook_TimesOutAndSurvives(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bus, clock, reg, blockingCfg, liveCfg := setUpBlockingHookFixture(t)

	pd := &fakePluginDispatcher{block: true, blocked: make(chan struct{})}
	nw := &fakeNoteWriter{}
	const shortTimeout = 30 * time.Millisecond
	d := newTestDispatcher(t, reg, bus, clock, pd, nw, shortTimeout)

	auditSub, err := bus.Subscribe(ctx, "audit", "observer3", 8)
	if err != nil {
		t.Fatalf("Subscribe(audit): %v", err)
	}

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	go func() { _ = d.Run(runCtx) }()

	if _, err := bus.Publish(ctx, "triggers", "plugin.registered", "test", nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case <-pd.blocked:
	case <-ctx.Done():
		t.Fatal("blocking action never started")
	}

	fire := awaitAuditFire(t, ctx, auditSub, blockingCfg.ID)
	if fire.ResultCode != ResultTimeout {
		t.Fatalf("ResultCode = %q, want timeout", fire.ResultCode)
	}

	// The engine must still be alive: a second, unrelated trigger fires
	// its own hook normally.
	if _, err := bus.Publish(ctx, "triggers", "scheduler.tick", "test", nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	liveFire := awaitAuditFire(t, ctx, auditSub, liveCfg.ID)
	if liveFire.ResultCode != ResultSuccess {
		t.Fatalf("live hook ResultCode = %q, want success (engine must have survived the blocker)", liveFire.ResultCode)
	}
}

// setUpBlockingHookFixture registers the two hooks
// TestDispatcher_BlockingHook_TimesOutAndSurvives needs — the permanently
// blocking one and a second, well-behaved one on a different trigger that
// proves the engine kept processing events after the blocker's timeout,
// rather than the whole Run loop having wedged. Split out purely to keep
// the test function itself under Art.10.3's 50-line cap.
func setUpBlockingHookFixture(t *testing.T) (bus *events.Bus, clock *testkit.FrozenClock, reg *Registry, blockingCfg, liveCfg HookConfig) {
	t.Helper()
	bus, clock = testBus(t)
	reg = NewRegistry()

	var err error
	blockingCfg, err = reg.Register(HookConfig{
		ID:         "blocker",
		Trigger:    "plugin.registered",
		ActionType: ActionTypePluginCall,
	})
	if err != nil {
		t.Fatalf("Register(blocker): %v", err)
	}
	liveCfg, err = reg.Register(HookConfig{
		ID:         "still-alive",
		Trigger:    "scheduler.tick",
		ActionType: ActionTypeAgentNote,
	})
	if err != nil {
		t.Fatalf("Register(still-alive): %v", err)
	}
	return bus, clock, reg, blockingCfg, liveCfg
}
