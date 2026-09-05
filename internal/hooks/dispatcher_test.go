// Purpose: Dispatcher coverage, part 1/2 (dispatcher_timeout_test.go is
//
//	part 2/2 — an R-14.117 same-package split of what was originally one
//	file, purely to satisfy Art.10.3's 300-line cap; the split is
//	behaviour-preserving, moved code only, no rewrites) — shell-action
//	dispatch refusal (defense-in-depth, exercised white-box since a
//	HookConfig carrying ActionTypeShell can never reach the dispatcher
//	via Registry.Register, which already refuses it — this file
//	constructs one directly and drives Dispatcher.runAction to prove the
//	SECOND guard), and audit-on-fire for success/dispatch-error. Shared
//	fixtures (fakes, testBus, awaitAuditFire, newTestDispatcher) live
//	here and are used by both files via the shared package hooks
//	white-box test binary.
//
// Inputs: a real *events.Bus over storetest.NewMemStore() (Art.7.1 — no
//
//	filesystem use, so no t.TempDir() is needed; MemStore never touches
//	disk) and a testkit.FrozenClock, per this repo's established events
//	package test convention (internal/events/bus_test.go).
//
// Constraints: white-box (package hooks, not hooks_test) — the shell
//
//	dispatch-refusal test needs runAction directly, since Register (the
//	only path an external caller has to populate a Registry) already
//	refuses shell before a Dispatcher ever sees it. No test in either
//	split file uses time.Sleep for synchronisation (R-14.136/Art.11) —
//	every wait is a channel receive, except the one short, explicit
//	ActionTimeout in dispatcher_timeout_test.go's
//	TestDispatcher_BlockingHook_TimesOutAndSurvives, which IS the feature
//	under test, not a sleep standing in for synchronisation.
//
// SPORT: internal.hooks.Dispatcher/ADDED (tests, split 1/2)
//
//	(P1-E03-W1-S05-T1).
package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakePluginDispatcher is a test-only PluginDispatcher. FORWARD NOTE: the
// real implementation is wired by C/S-05.T7's plugin registry
// (composition-root work, not this ticket) — see hooks.go's
// PluginDispatcher doc.
type fakePluginDispatcher struct {
	calls   []string
	err     error
	block   bool // if true, DispatchPluginCall never returns
	blocked chan struct{}
}

func (f *fakePluginDispatcher) DispatchPluginCall(_ context.Context, hookID string, _ map[string]string) error {
	f.calls = append(f.calls, hookID)
	if f.block {
		if f.blocked != nil {
			close(f.blocked)
		}
		select {} // deliberately ignores ctx cancellation: proves the
		// dispatcher bounds a hook that never returns, not just one that
		// respects context.
	}
	return f.err
}

// fakeNoteWriter is a test-only NoteWriter. FORWARD NOTE: NO production
// implementation exists anywhere in this tree yet — it is wired once
// G/S-13 (the journal/memory domain) ships. See hooks.go's NoteWriter doc.
type fakeNoteWriter struct {
	calls []string
	err   error
	panic bool
}

func (f *fakeNoteWriter) WriteAgentNote(_ context.Context, hookID string, _ map[string]string) error {
	f.calls = append(f.calls, hookID)
	if f.panic {
		panic("fakeNoteWriter: deliberate test panic")
	}
	return f.err
}

// testBus builds a real, in-memory-backed events.Bus and a frozen clock
// for a dispatcher test.
func testBus(t *testing.T) (*events.Bus, *testkit.FrozenClock) {
	t.Helper()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })
	return bus, clock
}

// awaitAuditFire reads from an audit subscription's Events channel until
// it sees a HookFire for wantHookID or ctx is done. t leads the parameter
// list per Go's *testing.T-first test-helper convention, which takes
// precedence over context-as-first-arg for helpers of this kind.
//
//nolint:revive // t-first is the idiomatic test-helper signature, not a context-ordering bug
func awaitAuditFire(t *testing.T, ctx context.Context, sub *events.Subscription, wantHookID string) HookFire {
	t.Helper()
	for {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				t.Fatalf("audit subscription closed before a HookFire for %q arrived", wantHookID)
			}
			var fire HookFire
			if err := json.Unmarshal(ev.Payload, &fire); err != nil {
				t.Fatalf("decode HookFire: %v", err)
			}
			if fire.HookID == wantHookID {
				return fire
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for a HookFire for %q", wantHookID)
		}
	}
}

func newTestDispatcher(t *testing.T, reg *Registry, bus *events.Bus, clock *testkit.FrozenClock, pd PluginDispatcher, nw NoteWriter, timeout time.Duration) *Dispatcher {
	t.Helper()
	fw, tok := testFirewall(t)
	d, err := NewDispatcher(DispatcherConfig{
		Registry:         reg,
		Bus:              bus,
		Clock:            clock,
		Egress:           fw,
		EgressToken:      tok,
		PluginDispatcher: pd,
		NoteWriter:       nw,
		ActionTimeout:    timeout,
		TriggerNamespace: "triggers",
		AuditNamespace:   "audit",
		CursorName:       "dispatcher",
		SubscribeBuffer:  8,
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	return d
}

// TestHooksShellActionDispatchRefused proves the SECOND, defense-in-depth
// refusal: runAction refuses ActionTypeShell (and any unrecognised type)
// immediately, before either PluginDispatcher or NoteWriter is ever
// called — exercised directly because Registry.Register (the only route
// an external caller has) already refuses shell at registration, so a
// black-box test could never construct this situation through the public
// API. hook is never stored in any Registry here — it is invoked exactly
// as runAction would receive it had some other, non-Register path (e.g. a
// future config-reload seam) stored it unvalidated.
func TestHooksShellActionDispatchRefused(t *testing.T) {
	pd := &fakePluginDispatcher{}
	nw := &fakeNoteWriter{}
	d := &Dispatcher{
		pluginDispatcher: pd,
		noteWriter:       nw,
	}

	outcome := d.runAction(context.Background(), HookConfig{
		ID:         "shell-hook",
		Trigger:    "t",
		ActionType: ActionTypeShell,
	})
	if outcome.result != ResultRefused {
		t.Fatalf("result = %q, want refused", outcome.result)
	}
	if outcome.err == nil {
		t.Fatal("outcome.err is nil, want the action-not-permitted error")
	}
	if kind, ok := cascade.KindOf(outcome.err); !ok || kind != cascade.KindPolicyDenied {
		t.Fatalf("outcome.err kind mismatch: %v", outcome.err)
	}
	if len(pd.calls) != 0 || len(nw.calls) != 0 {
		t.Fatalf("PluginDispatcher/NoteWriter were called: pd=%v nw=%v, want neither called", pd.calls, nw.calls)
	}

	// Unknown, never-registrable action type takes the identical path.
	pd2 := &fakePluginDispatcher{}
	nw2 := &fakeNoteWriter{}
	d2 := &Dispatcher{pluginDispatcher: pd2, noteWriter: nw2}
	outcome2 := d2.runAction(context.Background(), HookConfig{ID: "unknown-hook", Trigger: "t", ActionType: ActionType("carrier-pigeon")})
	if outcome2.result != ResultRefused {
		t.Fatalf("result = %q, want refused for unknown action type", outcome2.result)
	}
	if len(pd2.calls) != 0 || len(nw2.calls) != 0 {
		t.Fatal("unknown action type reached an injected dispatcher")
	}
}

func TestHooksAuditEventEmittedOnFire(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bus, clock := testBus(t)

	reg := NewRegistry()
	cfg, err := reg.Register(HookConfig{
		ID:           "plugin-hook",
		Trigger:      "plugin.registered",
		ActionType:   ActionTypePluginCall,
		ActionParams: map[string]string{"plugin": "p1"},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	pd := &fakePluginDispatcher{}
	d := newTestDispatcher(t, reg, bus, clock, pd, &fakeNoteWriter{}, time.Second)

	auditSub, err := bus.Subscribe(ctx, "audit", "test-observer", 8)
	if err != nil {
		t.Fatalf("Subscribe(audit): %v", err)
	}

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	go func() { _ = d.Run(runCtx) }()

	if _, err := bus.Publish(ctx, "triggers", "plugin.registered", "test", nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	fire := awaitAuditFire(t, ctx, auditSub, cfg.ID)
	if fire.ResultCode != ResultSuccess {
		t.Fatalf("ResultCode = %q, want success", fire.ResultCode)
	}
	if fire.Trigger != "plugin.registered" || fire.ActionType != ActionTypePluginCall {
		t.Fatalf("fire = %+v, unexpected trigger/action_type", fire)
	}
	if fire.ParamsHash == "" {
		t.Fatal("ParamsHash is empty")
	}
	if fire.Ts.IsZero() {
		t.Fatal("Ts is zero")
	}
	if len(pd.calls) != 1 || pd.calls[0] != cfg.ID {
		t.Fatalf("PluginDispatcher calls = %v, want [%s]", pd.calls, cfg.ID)
	}
}

func TestHooksAuditEventEmittedOnDispatchError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bus, clock := testBus(t)

	reg := NewRegistry()
	cfg, err := reg.Register(HookConfig{
		ID:         "note-hook-err",
		Trigger:    "scheduler.tick",
		ActionType: ActionTypeAgentNote,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	wantErr := errors.New("journal unavailable")
	nw := &fakeNoteWriter{err: wantErr}
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
	if fire.ResultCode != ResultError {
		t.Fatalf("ResultCode = %q, want error", fire.ResultCode)
	}
	if fire.ErrMsg == "" {
		t.Fatal("ErrMsg empty on a dispatch-error fire")
	}
}
