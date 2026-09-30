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
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/events/routing"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// stubRouter returns one fixed verdict and records every action it saw.
// order, when set, receives "route" on each call so a test can check the
// router ran before the rehydration seam.
type stubRouter struct {
	mu      sync.Mutex
	verdict policy.Verdict
	err     error
	seen    []routing.Action
	order   *callOrder
}

func (s *stubRouter) RouteAction(_ context.Context, action routing.Action) (policy.Verdict, policy.Trace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, action)
	s.order.add("route")
	return s.verdict, policy.Trace{}, s.err
}

func (s *stubRouter) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}

func (s *stubRouter) actions() []routing.Action {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]routing.Action(nil), s.seen...)
}

// callOrder records stage names across goroutines; a nil *callOrder
// records nothing.
type callOrder struct {
	mu    sync.Mutex
	names []string
}

func (o *callOrder) add(name string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.names = append(o.names, name)
}

// fakeShellRunner is a test-only ShellRunner. No production implementation
// exists anywhere in this tree yet.
type fakeShellRunner struct {
	mu    sync.Mutex
	calls []string
	cmds  []string
	err   error
}

func (f *fakeShellRunner) RunShell(_ context.Context, hookID, command string, _ map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, hookID)
	f.cmds = append(f.cmds, command)
	return f.err
}

func (f *fakeShellRunner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// fireShellHook registers a shell hook on a dispatcher wired with router
// and sh, publishes its trigger on the real bus, and returns the HookFire.
func fireShellHook(t *testing.T, router *stubRouter, sh *fakeShellRunner) HookFire {
	t.Helper()
	r := newRig(t)
	r.router = router
	r.register(HookConfig{ID: "shell-hook", Namespace: "plugins", Trigger: "plugin.registered",
		ActionType: ActionTypeShell, ActionParams: map[string]string{ShellCommandParam: "cat notes.txt"}})
	r.build(func(c *DispatcherConfig) { c.ShellRunner, c.ShellCapability = sh, "hooks.shell" })
	sub := r.audit()
	r.start()
	r.publish("plugins", "plugin.registered")
	fire, _ := nextFire(t, sub)
	return fire
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
	if sh.count() != 1 || sh.cmds[0] != "cat notes.txt" {
		t.Fatalf("ShellRunner calls = %v %v, want the action dispatched once", sh.calls, sh.cmds)
	}
	if router.count() != 1 {
		t.Fatalf("router saw %d actions, want exactly 1", router.count())
	}
	got := router.actions()[0]
	if got.Origin != routing.OriginHook || got.Ref != "shell-hook" || got.Command != "cat notes.txt" ||
		got.Capability != "hooks.shell" {
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
	if sh.count() != 0 {
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
	if sh.count() != 0 {
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
	if sh.count() != 0 {
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
	if sh.count() != 0 {
		t.Fatalf("ShellRunner ran on an unrecordable allow: %v", sh.calls)
	}
}

// TestHookShellActionMissingCommand proves a shell hook with no command
// parameter is refused rather than run with an empty command, which the
// classifier could not have reasoned about.
func TestHookShellActionMissingCommand(t *testing.T) {
	r := newRig(t)
	sh := &fakeShellRunner{}
	r.build(func(c *DispatcherConfig) { c.ShellRunner, c.ShellCapability = sh, "hooks.shell" })
	fire, _ := r.d.dispatchHook(context.Background(), HookConfig{ID: "no-cmd", Namespace: "jobs", ActionType: ActionTypeShell}, "jobs", 1)
	if fire.ResultCode != ResultRefused || fire.ErrMsg == "" {
		t.Fatalf("fire = %+v, want a refusal", fire)
	}
	if r.router.count() != 0 || sh.count() != 0 {
		t.Fatal("an action with no command text reached the policy engine or the runner")
	}
}

// TestShellUnrunnableWithoutShellRunner proves a dispatcher with no
// ShellRunner reports shell unrunnable and refuses a smuggled shell hook
// at dispatch before routing.
func TestShellUnrunnableWithoutShellRunner(t *testing.T) {
	r := newRig(t)
	r.build(nil)
	if r.d.Runnable(ActionTypeShell) {
		t.Fatal("shell runnable on a dispatcher with no ShellRunner")
	}
	fire, err := r.d.dispatchHook(context.Background(), HookConfig{ID: "sh", Namespace: "jobs", ActionType: ActionTypeShell,
		ActionParams: map[string]string{ShellCommandParam: "ls"}}, "jobs", 1)
	if fire.ResultCode != ResultRefused || !strings.Contains(err.Error(), HookActionNotPermittedCode) {
		t.Fatalf("fire = %+v err = %v", fire, err)
	}
	if r.router.count() != 0 || r.seam.count() != 0 {
		t.Fatal("an unrunnable shell hook reached routing or the seam")
	}
}
