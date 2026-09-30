// Purpose: dispatcher bounds and construction: panic and timeout outcomes,
//
//	trigger misses, config validation, Run error propagation, and a hook
//	that blocks forever never wedging the loop, and a timed-out fire never
//	reaching a later stage.
package hooks

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events/routing"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/policy"
)

// TestDispatcher_PanickingHook_AuditsPanic proves a panicking runner is
// recovered and recorded as panic.
func TestDispatcher_PanickingHook_AuditsPanic(t *testing.T) {
	r := newRig(t)
	r.note.panics = true
	r.register(HookConfig{ID: "boom", Namespace: "scheduler", Trigger: "tick", ActionType: ActionTypeAgentNote})
	r.build(nil)
	sub := r.audit()
	r.start()
	r.publish("scheduler", "tick")
	if fire, _ := nextFire(t, sub); fire.ResultCode != ResultPanic || fire.ErrMsg == "" {
		t.Fatalf("fire = %+v, want panic with a message", fire)
	}
}

// TestDispatcher_TriggerMatching_Miss_NoAudit proves a non-matching kind,
// and a matching kind on a namespace no hook names, dispatch nothing.
func TestDispatcher_TriggerMatching_Miss_NoAudit(t *testing.T) {
	r := newRig(t)
	r.register(HookConfig{ID: "h", Namespace: "jobs", Trigger: "wanted", ActionType: ActionTypePluginCall})
	r.build(nil)
	sub := r.audit()
	r.start()
	r.publish("jobs", "unwanted")
	r.publish("other", "wanted")
	r.publish("jobs", "wanted")
	if fire, _ := nextFire(t, sub); fire.HookID != "h" || fire.ResultCode != ResultSuccess {
		t.Fatalf("fire = %+v", fire)
	}
	noMoreFires(t, sub)
	if r.plugin.count() != 1 {
		t.Fatalf("runner count = %d, want 1", r.plugin.count())
	}
}

// TestNewDispatcher_ValidatesEveryRequiredField refuses construction with
// each required field broken alone.
func TestNewDispatcher_ValidatesEveryRequiredField(t *testing.T) {
	r := newRig(t)
	cases := map[string]func(*DispatcherConfig){
		"Registry":        func(c *DispatcherConfig) { c.Registry = nil },
		"Bus":             func(c *DispatcherConfig) { c.Bus = nil },
		"Clock":           func(c *DispatcherConfig) { c.Clock = nil },
		"Egress":          func(c *DispatcherConfig) { c.Egress = nil },
		"EgressToken":     func(c *DispatcherConfig) { c.EgressToken = egress.Capability{} },
		"Runners empty":   func(c *DispatcherConfig) { c.Runners = nil },
		"Router":          func(c *DispatcherConfig) { c.Router = nil },
		"RouteSubject":    func(c *DispatcherConfig) { c.RouteSubject = policy.Subject{} },
		"State":           func(c *DispatcherConfig) { c.State = nil },
		"ActionTimeout":   func(c *DispatcherConfig) { c.ActionTimeout = 0 },
		"AuditNamespace":  func(c *DispatcherConfig) { c.AuditNamespace = "audit" },
		"CursorPrefix":    func(c *DispatcherConfig) { c.CursorPrefix = "" },
		"SubscribeBuffer": func(c *DispatcherConfig) { c.SubscribeBuffer = 0 },
		"shell runner key": func(c *DispatcherConfig) {
			c.Runners[ActionTypeShell] = &fakeRunner{}
			c.ActionCapabilities[ActionTypeShell] = "hooks.shell"
		},
		"nil runner":           func(c *DispatcherConfig) { c.Runners[ActionTypeAgentNote] = nil },
		"PluginCallRunner nil": func(c *DispatcherConfig) { c.Runners[ActionTypePluginCall] = PluginCallRunner(nil) },
		"AgentNoteRunner nil":  func(c *DispatcherConfig) { c.Runners[ActionTypeAgentNote] = AgentNoteRunner(nil) },
		"missing capability":   func(c *DispatcherConfig) { delete(c.ActionCapabilities, ActionTypeAgentNote) },
		"capability no runner": func(c *DispatcherConfig) { c.ActionCapabilities["extra"] = "x" },
		"shell half wired":     func(c *DispatcherConfig) { c.ShellCapability = "hooks.shell" },
		"shell runner no cap":  func(c *DispatcherConfig) { c.ShellRunner = &fakeShellRunner{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := r.config()
			mutate(&cfg)
			if _, err := NewDispatcher(cfg); err == nil {
				t.Fatalf("NewDispatcher accepted a config with %s broken", name)
			}
		})
	}
	if _, err := NewDispatcher(r.config()); err != nil {
		t.Fatalf("NewDispatcher(valid) = %v", err)
	}
}

// TestDispatcher_Run_SubscribeError proves Run propagates a subscription
// it cannot take (the cursor is held elsewhere) and a second concurrent
// Run is refused.
func TestDispatcher_Run_SubscribeError(t *testing.T) {
	r := newRig(t)
	r.register(HookConfig{ID: "h", Namespace: "jobs", Trigger: "k", ActionType: ActionTypePluginCall})
	r.build(nil)
	held, err := r.bus.Subscribe(context.Background(), "jobs", "hooks:jobs", 1)
	if err != nil {
		t.Fatalf("Subscribe(holder): %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.d.Run(ctx); err == nil {
		t.Fatal("Run returned nil for a held cursor")
	}
	_ = held.Unsubscribe()
	r.start()
	if err := r.d.Run(ctx); err == nil {
		t.Fatal("a second concurrent Run was accepted")
	}
}

// TestDispatcher_ZeroHooks_NoOp proves an empty registry runs, dispatches
// nothing and exits cleanly on cancel.
func TestDispatcher_ZeroHooks_NoOp(t *testing.T) {
	r := newRig(t)
	r.build(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.d.Run(ctx) }()
	r.publish("jobs", "anything")
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit after cancel")
	}
	if r.plugin.count()+r.note.count() != 0 {
		t.Fatal("an empty registry dispatched")
	}
}

// TestDispatcher_BlockingHook_TimesOutAndSurvives proves a hook that
// blocks forever, ignoring its context, is recorded as timeout and the
// loop keeps dispatching.
func TestDispatcher_BlockingHook_TimesOutAndSurvives(t *testing.T) {
	r := newRig(t)
	r.plugin.block = make(chan struct{})
	r.register(HookConfig{ID: "blocker", Namespace: "jobs", Trigger: "a", ActionType: ActionTypePluginCall})
	r.register(HookConfig{ID: "alive", Namespace: "jobs", Trigger: "b", ActionType: ActionTypeAgentNote})
	r.build(func(c *DispatcherConfig) { c.ActionTimeout = 30 * time.Millisecond })
	sub := r.audit()
	r.start()
	r.publish("jobs", "a")
	<-r.plugin.block
	if fire, _ := nextFire(t, sub); fire.HookID != "blocker" || fire.ResultCode != ResultTimeout {
		t.Fatalf("fire = %+v, want blocker timeout", fire)
	}
	r.publish("jobs", "b")
	if fire, _ := nextFire(t, sub); fire.HookID != "alive" || fire.ResultCode != ResultSuccess {
		t.Fatalf("fire = %+v, want the live hook to succeed", fire)
	}
}

// gatedRouter is the rig's router held at a gate, then signalling done.
type gatedRouter struct {
	*stubRouter
	gate, done chan struct{}
}

func (g gatedRouter) RouteAction(ctx context.Context, a routing.Action) (policy.Verdict, policy.Trace, error) {
	defer close(g.done)
	<-g.gate
	return g.stubRouter.RouteAction(ctx, a)
}

// TestTimedOutFireNeverReachesLaterStages proves a router or seam that
// outlives ActionTimeout ends the fire as timeout and the abandoned
// pipeline never calls a later stage: no seam after a late router, no
// runner after a late seam.
func TestTimedOutFireNeverReachesLaterStages(t *testing.T) {
	for _, stage := range []string{"router", "seam"} {
		t.Run(stage, func(t *testing.T) {
			r := newRig(t)
			gate, done := make(chan struct{}), make(chan struct{})
			r.build(func(c *DispatcherConfig) {
				c.ActionTimeout = 30 * time.Millisecond
				if stage == "router" {
					c.Router = gatedRouter{stubRouter: r.router, gate: gate, done: done}
					return
				}
				c.Rehydrate = func(ctx context.Context, f Fire, tagged map[string]string) (map[string]string, func(), error) {
					defer close(done)
					<-gate
					return r.seam.seam(ctx, f, tagged)
				}
			})
			fire, _ := r.d.dispatchHook(context.Background(), credHook, "jobs", 1)
			close(gate)
			<-done
			time.Sleep(200 * time.Millisecond) // the abandoned pipeline's remaining steps
			seamCalls := r.seam.count()
			if fire.ResultCode != ResultTimeout || r.plugin.count() != 0 || (stage == "router" && seamCalls != 0) {
				t.Fatalf("result %q, runner calls %d, seam calls %d; want timeout and no later stage",
					fire.ResultCode, r.plugin.count(), seamCalls)
			}
		})
	}
}
