package hooks

// Purpose: the routed shell-action path, driven through the REAL entry
//   point — a real events.Bus publish that Dispatcher.Run picks up,
//   matches against a real Registry, and dispatches. Removing the
//   RouteAction call from shell_route.go turns TestHookShellActionDeny
//   and TestHookShellActionAsk red, which is what makes these tests proof
//   that the routing is wired rather than proof that a stub was called.
// Constraints: the expected allow/ask/deny behaviour is transcribed from
//   06-FORGE-SPEC §5.15 as the router's own tests assert it; the stub
//   router here returns one fixed verdict per test so the CALL SITE's
//   handling of each verdict is what is under test.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events/routing"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// stubRouter returns one fixed verdict and records every action it saw.
type stubRouter struct {
	verdict policy.Verdict
	err     error
	seen    []routing.Action
}

func (s *stubRouter) RouteAction(_ context.Context, action routing.Action) (policy.Verdict, policy.Trace, error) {
	s.seen = append(s.seen, action)
	return s.verdict, policy.Trace{}, s.err
}

// fakeShellRunner is a test-only ShellRunner. FORWARD NOTE: no production
// implementation exists anywhere in this tree yet, on the same terms as
// NoteWriter.
type fakeShellRunner struct {
	calls []string
	err   error
}

func (f *fakeShellRunner) RunShell(_ context.Context, hookID, _ string, _ map[string]string) error {
	f.calls = append(f.calls, hookID)
	return f.err
}

func routedShellDispatcher(t *testing.T, reg *Registry, router ActionRouter, sh ShellRunner) *Dispatcher {
	t.Helper()
	bus, clock := testBus(t)
	fw, tok := testFirewall(t)
	d, err := NewDispatcher(DispatcherConfig{
		Registry: reg, Bus: bus, Clock: clock, Egress: fw, EgressToken: tok,
		PluginDispatcher: &fakePluginDispatcher{}, NoteWriter: &fakeNoteWriter{},
		Router: router, ShellRunner: sh,
		RouteSubject:    policy.Subject{Kind: policy.SubjectAgent, ID: "hooks"},
		ShellCapability: "hooks.shell",
		ActionTimeout:   time.Second, TriggerNamespace: "triggers",
		AuditNamespace: "audit", CursorName: "dispatcher", SubscribeBuffer: 8,
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	return d
}

// fireShellHook registers a shell hook, publishes its trigger on the real
// bus, runs the dispatcher, and returns the HookFire audit record.
func fireShellHook(t *testing.T, router ActionRouter, sh ShellRunner) HookFire {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	reg := NewRegistry()
	cfg, err := reg.Register(HookConfig{
		ID: "shell-hook", Trigger: "plugin.registered", ActionType: ActionTypeShell,
		ActionParams: map[string]string{ShellCommandParam: "cat notes.txt"},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	d := routedShellDispatcher(t, reg, router, sh)
	sub, err := d.bus.Subscribe(ctx, "audit", "test-observer", 8)
	if err != nil {
		t.Fatalf("Subscribe(audit): %v", err)
	}
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	go func() { _ = d.Run(runCtx) }()
	if _, err := d.bus.Publish(ctx, "triggers", "plugin.registered", "test", nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return awaitAuditFire(t, ctx, sub, cfg.ID)
}

// TestHookShellActionAllow proves an allowed shell action reaches the
// runner and audits as a success.
func TestHookShellActionAllow(t *testing.T) {
	router := &stubRouter{verdict: policy.VerdictAllow}
	sh := &fakeShellRunner{}
	fire := fireShellHook(t, router, sh)
	if fire.ResultCode != ResultSuccess {
		t.Fatalf("ResultCode = %q, want success", fire.ResultCode)
	}
	if len(sh.calls) != 1 {
		t.Fatalf("ShellRunner calls = %v, want the action dispatched once", sh.calls)
	}
	if len(router.seen) != 1 {
		t.Fatalf("router saw %d actions, want exactly 1", len(router.seen))
	}
	got := router.seen[0]
	if got.Origin != routing.OriginHook || got.Ref != "shell-hook" || got.Command != "cat notes.txt" {
		t.Fatalf("routed action = %+v, want the hook's origin, ref and command", got)
	}
}

// TestHookShellActionDeny proves a denied shell action never reaches the
// runner and returns a typed refusal.
func TestHookShellActionDeny(t *testing.T) {
	sh := &fakeShellRunner{}
	fire := fireShellHook(t, &stubRouter{
		verdict: policy.VerdictDeny,
		err:     cascade.New(cascade.KindPolicyDenied, "routing: refused"),
	}, sh)
	if fire.ResultCode != ResultRefused {
		t.Fatalf("ResultCode = %q, want refused", fire.ResultCode)
	}
	if len(sh.calls) != 0 {
		t.Fatalf("ShellRunner was called on a deny: %v", sh.calls)
	}
	if fire.ErrMsg == "" {
		t.Fatal("a denied dispatch recorded no reason")
	}
}

// TestHookShellActionAsk proves an ask suspends the dispatch: the runner
// is not called and the fire records the pending state, distinct from a
// refusal.
func TestHookShellActionAsk(t *testing.T) {
	sh := &fakeShellRunner{}
	fire := fireShellHook(t, &stubRouter{verdict: policy.VerdictAsk}, sh)
	if fire.ResultCode != ResultAsk {
		t.Fatalf("ResultCode = %q, want ask", fire.ResultCode)
	}
	if len(sh.calls) != 0 {
		t.Fatalf("ShellRunner was called on an ask: %v", sh.calls)
	}
}

// TestHookShellActionRouterError proves a routing call that fails without
// producing a readable verdict refuses: the unavailable case is a deny,
// never a dispatch.
func TestHookShellActionRouterError(t *testing.T) {
	sh := &fakeShellRunner{}
	fire := fireShellHook(t, &stubRouter{verdict: policy.Verdict(0), err: errors.New("engine unavailable")}, sh)
	if fire.ResultCode != ResultRefused {
		t.Fatalf("ResultCode = %q, want refused", fire.ResultCode)
	}
	if len(sh.calls) != 0 {
		t.Fatalf("ShellRunner was called after a routing failure: %v", sh.calls)
	}
}

// TestHookShellActionAllowWithError proves an allow verdict returned
// alongside an error still refuses: an allow nobody could record is not
// an allow.
func TestHookShellActionAllowWithError(t *testing.T) {
	sh := &fakeShellRunner{}
	fire := fireShellHook(t, &stubRouter{verdict: policy.VerdictAllow, err: errors.New("audit unavailable")}, sh)
	if fire.ResultCode != ResultRefused {
		t.Fatalf("ResultCode = %q, want refused", fire.ResultCode)
	}
	if len(sh.calls) != 0 {
		t.Fatalf("ShellRunner ran on an unrecordable allow: %v", sh.calls)
	}
}

// TestHookShellActionMissingCommand proves a shell hook with no command
// parameter is refused rather than run with an empty command, which the
// classifier could not have reasoned about.
func TestHookShellActionMissingCommand(t *testing.T) {
	router := &stubRouter{verdict: policy.VerdictAllow}
	sh := &fakeShellRunner{}
	d := routedShellDispatcher(t, NewRegistry(), router, sh)
	outcome := d.runAction(context.Background(), HookConfig{ID: "no-cmd", ActionType: ActionTypeShell})
	if outcome.result != ResultRefused || outcome.err == nil {
		t.Fatalf("outcome = %+v, want a refusal", outcome)
	}
	if len(router.seen) != 0 {
		t.Fatal("an action with no command text reached the policy engine")
	}
}

// TestHookShellActionPluginCallBypasses proves plugin-call and agent-note
// are NOT routed: they are pre-approved at load time, so the router must
// see nothing when one fires.
func TestHookShellActionPluginCallBypasses(t *testing.T) {
	router := &stubRouter{verdict: policy.VerdictDeny}
	d := routedShellDispatcher(t, NewRegistry(), router, &fakeShellRunner{})
	for _, actionType := range []ActionType{ActionTypePluginCall, ActionTypeAgentNote} {
		outcome := d.runAction(context.Background(), HookConfig{ID: "pre-approved", ActionType: actionType})
		if outcome.result != ResultSuccess {
			t.Fatalf("%s: result = %q, want success", actionType, outcome.result)
		}
	}
	if len(router.seen) != 0 {
		t.Fatalf("pre-approved action types reached the router: %+v", router.seen)
	}
}

// TestNewDispatcherRefusesHalfWiredRouting proves a router with no runner
// or no capability is refused at construction rather than at the first
// shell action.
func TestNewDispatcherRefusesHalfWiredRouting(t *testing.T) {
	bus, clock := testBus(t)
	fw, tok := testFirewall(t)
	base := DispatcherConfig{
		Registry: NewRegistry(), Bus: bus, Clock: clock, Egress: fw, EgressToken: tok,
		PluginDispatcher: &fakePluginDispatcher{}, NoteWriter: &fakeNoteWriter{},
		Router: &stubRouter{}, ShellRunner: &fakeShellRunner{}, ShellCapability: "hooks.shell",
		ActionTimeout: time.Second, TriggerNamespace: "triggers",
		AuditNamespace: "audit", CursorName: "dispatcher", SubscribeBuffer: 8,
	}
	noRunner := base
	noRunner.ShellRunner = nil
	if _, err := NewDispatcher(noRunner); err == nil {
		t.Fatal("dispatcher built with a router and no shell runner")
	}
	noCap := base
	noCap.ShellCapability = ""
	if _, err := NewDispatcher(noCap); err == nil {
		t.Fatal("dispatcher built with a router and no capability")
	}
}
