// Purpose: the shared dispatcher test rig (a real bus over a MemStore, a
//
//	frozen clock, recording runners, a recording rehydration seam and an
//	allow-by-default router) plus the basic fire and audit tests.
//
// Constraints: white-box (package hooks). Waits are channel receives
//
//	bounded by a context; the only explicit durations are action timeouts
//	under test and the bounded "nothing else arrives" windows.
package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

// fakeRunner records every fire and the params it was handed. onRun, when
// set, runs inside RunAction with the runner's own context.
type fakeRunner struct {
	mu     sync.Mutex
	fires  []Fire
	params []map[string]string
	err    error
	panics bool
	block  chan struct{}
	order  *callOrder
	onRun  func(ctx context.Context, fire Fire, params map[string]string) error
}

func (r *fakeRunner) RunAction(ctx context.Context, fire Fire, params map[string]string) error {
	r.mu.Lock()
	r.fires = append(r.fires, fire)
	r.params = append(r.params, cloneParams(params))
	onRun, err := r.onRun, r.err
	r.mu.Unlock()
	r.order.add("run")
	if r.panics {
		panic("fakeRunner: deliberate test panic")
	}
	if r.block != nil {
		close(r.block)
		select {} // ignores ctx on purpose: the dispatcher must bound it anyway
	}
	if onRun != nil {
		return onRun(ctx, fire, params)
	}
	return err
}

func (r *fakeRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.fires)
}

func (r *fakeRunner) snapshot() ([]Fire, []map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Fire(nil), r.fires...), append([]map[string]string(nil), r.params...)
}

// fakeSeam is a recording rehydration seam. transform maps tagged to plain
// (identity copy when nil); zeros counts zero() calls.
type fakeSeam struct {
	mu        sync.Mutex
	calls     []map[string]string
	transform func(map[string]string) map[string]string
	err       error
	order     *callOrder
	zeros     atomic.Int32
}

func (s *fakeSeam) seam(_ context.Context, _ Fire, tagged map[string]string) (map[string]string, func(), error) {
	s.mu.Lock()
	s.calls = append(s.calls, cloneParams(tagged))
	transform, err := s.transform, s.err
	s.mu.Unlock()
	s.order.add("seam")
	plain := cloneParams(tagged)
	if transform != nil {
		plain = transform(tagged)
	}
	return plain, func() { s.zeros.Add(1) }, err
}

func (s *fakeSeam) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// rig is one dispatcher under test.
type rig struct {
	t      *testing.T
	bus    *events.Bus
	clock  *testkit.FrozenClock
	store  *storetest.MemStore
	state  *storetest.MemStore
	reg    *Registry
	router *stubRouter
	seam   *fakeSeam
	plugin *fakeRunner
	note   *fakeRunner
	egress Interceptor
	d      *Dispatcher
}

func newRig(t *testing.T) *rig {
	t.Helper()
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(store, clock)
	t.Cleanup(func() { _ = bus.Close() })
	return &rig{
		t: t, bus: bus, clock: clock, store: store, state: storetest.NewMemStore(), reg: NewRegistry(),
		router: &stubRouter{verdict: policy.VerdictAllow}, seam: &fakeSeam{},
		plugin: &fakeRunner{}, note: &fakeRunner{},
	}
}

// config is a complete, valid DispatcherConfig for the rig.
func (r *rig) config() DispatcherConfig {
	fw, tok := testFirewall(r.t)
	if r.egress != nil {
		fw = r.egress
	}
	return DispatcherConfig{
		Registry: r.reg, Bus: r.bus, Clock: r.clock, Egress: fw, EgressToken: tok,
		Runners:            map[ActionType]ActionRunner{ActionTypePluginCall: r.plugin, ActionTypeAgentNote: r.note},
		ActionCapabilities: map[ActionType]string{ActionTypePluginCall: "hooks.plugin", ActionTypeAgentNote: "hooks.note"},
		Rehydrate:          r.seam.seam, Router: r.router,
		RouteSubject: policy.Subject{Kind: policy.SubjectAgent, ID: "hooks"}, State: r.state,
		ActionTimeout: 3 * time.Second, AuditNamespace: AuditNamespace, CursorPrefix: "hooks:", SubscribeBuffer: 64,
	}
}

// build constructs the dispatcher from cfg (the rig's config when nil).
func (r *rig) build(mutate func(*DispatcherConfig)) *Dispatcher {
	r.t.Helper()
	cfg := r.config()
	if mutate != nil {
		mutate(&cfg)
	}
	d, err := NewDispatcher(cfg)
	if err != nil {
		r.t.Fatalf("NewDispatcher: %v", err)
	}
	r.d = d
	return d
}

func (r *rig) register(cfg HookConfig) HookConfig {
	r.t.Helper()
	got, err := r.reg.Register(cfg)
	if err != nil {
		r.t.Fatalf("Register: %v", err)
	}
	return got
}

// start runs the dispatcher: subscriptions exist when start returns, so a
// publish right after it is never lost to the head-of-namespace start.
func (r *rig) start() {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errs, err := r.d.start(ctx)
	if err != nil {
		cancel()
		r.t.Fatalf("start: %v", err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = r.d.serve(ctx, errs) }()
	r.t.Cleanup(func() { cancel(); <-done })
}

func (r *rig) publish(ns, kind string) events.Event {
	r.t.Helper()
	ev, err := r.bus.Publish(context.Background(), ns, events.EventKind(kind), "test", nil)
	if err != nil {
		r.t.Fatalf("Publish: %v", err)
	}
	return ev
}

// audit subscribes to the hooks audit namespace from its first event.
func (r *rig) audit() *events.Subscription {
	r.t.Helper()
	sub, err := r.bus.Subscribe(context.Background(), AuditNamespace, "observer-"+r.t.Name(), 256)
	if err != nil {
		r.t.Fatalf("Subscribe(audit): %v", err)
	}
	return sub
}

// nextFire returns the next HookFire on sub, failing after 5s.
func nextFire(t *testing.T, sub *events.Subscription) (HookFire, events.Event) {
	t.Helper()
	select {
	case ev, ok := <-sub.Events:
		if !ok {
			t.Fatal("audit subscription closed")
		}
		var fire HookFire
		if err := json.Unmarshal(ev.Payload, &fire); err != nil {
			t.Fatalf("decode HookFire: %v", err)
		}
		return fire, ev
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a HookFire")
	}
	return HookFire{}, events.Event{}
}

// noMoreFires asserts nothing else reaches sub inside a short window.
func noMoreFires(t *testing.T, sub *events.Subscription) {
	t.Helper()
	select {
	case ev := <-sub.Events:
		t.Fatalf("unexpected extra audit event: %s", ev.Payload)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestHooksAuditEventEmittedOnFire proves a matched plugin-call fires,
// reaches its runner once, and records one success with the event identity.
func TestHooksAuditEventEmittedOnFire(t *testing.T) {
	r := newRig(t)
	cfg := r.register(HookConfig{ID: "p", Namespace: "plugins", Trigger: "plugin.registered",
		ActionType: ActionTypePluginCall, ActionParams: map[string]string{"plugin": "p1"}})
	r.build(nil)
	sub := r.audit()
	r.start()
	ev := r.publish("plugins", "plugin.registered")
	fire, _ := nextFire(t, sub)
	if fire.HookID != cfg.ID || fire.ResultCode != ResultSuccess || fire.Namespace != "plugins" ||
		fire.EventSeq != ev.Seq || fire.Depth != 0 || fire.ParamsHash == "" || fire.Ts.IsZero() {
		t.Fatalf("fire = %+v", fire)
	}
	if r.plugin.count() != 1 || r.note.count() != 0 {
		t.Fatalf("runner counts plugin=%d note=%d, want 1/0", r.plugin.count(), r.note.count())
	}
}

// TestHooksAuditEventEmittedOnDispatchError proves a runner error is
// recorded as error with its message.
func TestHooksAuditEventEmittedOnDispatchError(t *testing.T) {
	r := newRig(t)
	r.note.err = errors.New("journal unavailable")
	r.register(HookConfig{ID: "n", Namespace: "scheduler", Trigger: "tick", ActionType: ActionTypeAgentNote})
	r.build(nil)
	sub := r.audit()
	r.start()
	r.publish("scheduler", "tick")
	fire, _ := nextFire(t, sub)
	if fire.ResultCode != ResultError || !strings.Contains(fire.ErrMsg, "journal unavailable") {
		t.Fatalf("fire = %+v, want error with the runner's message", fire)
	}
}
