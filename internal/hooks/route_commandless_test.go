package hooks

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/events/routing"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

// observedPolicyRouter records the real router's input and result.
type observedPolicyRouter struct {
	*routing.ActionRouter
	action  routing.Action
	verdict policy.Verdict
	trace   policy.Trace
}

func (r *observedPolicyRouter) RouteAction(ctx context.Context, a routing.Action) (policy.Verdict, policy.Trace, error) {
	r.action = a
	v, trace, err := r.ActionRouter.RouteAction(ctx, a)
	r.verdict, r.trace = v, trace
	return v, trace, err
}

// hookPolicyRouter builds a real engine, grants and audit on an isolated file.
func hookPolicyRouter(t *testing.T, class policy.ActionClass, deny bool) *observedPolicyRouter {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "hooks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0).UTC())
	reg := policy.NewMemoryRegistry()
	for _, name := range []string{"hooks.plugin_call", "hooks.agent_note", "hooks.shell"} {
		if err := reg.Add(ctx, policy.Capability{Name: name, Desc: "hook test", DefaultPolicy: class}); err != nil {
			t.Fatal(err)
		}
	}
	grants, err := policy.NewStoreGrants(db, reg, clock)
	if err != nil {
		t.Fatal(err)
	}
	ctrl := policy.NewController(nil)
	if err := ctrl.Apply(ctx, map[string]interface{}{"policy": map[string]interface{}{"autonomy_profile": "balanced"}}); err != nil {
		t.Fatal(err)
	}
	engine, err := policy.NewEngine(reg, grants, ctrl)
	if err != nil {
		t.Fatal(err)
	}
	if deny {
		for _, name := range []string{"hooks.plugin_call", "hooks.agent_note"} {
			if err := grants.Grant(ctx, policy.Grant{
				Subject:    policy.Subject{Kind: policy.SubjectAgent, ID: "hooks"},
				Capability: name, ScopeClass: corpus.VisibilityTeam, Verdict: policy.VerdictDeny,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	router, err := routing.NewActionRouter(engine, audit.New(db, clock, nil))
	if err != nil {
		t.Fatal(err)
	}
	return &observedPolicyRouter{ActionRouter: router}
}

// TestHookPluginCallRoutesByCapabilityClass covers all policy outcomes.
func TestHookPluginCallRoutesByCapabilityClass(t *testing.T) {
	testHookCapabilityClass(t, ActionTypePluginCall, "hooks.plugin_call")
}

// TestHookAgentNoteRoutesByCapabilityClass covers all policy outcomes.
func TestHookAgentNoteRoutesByCapabilityClass(t *testing.T) {
	testHookCapabilityClass(t, ActionTypeAgentNote, "hooks.agent_note")
}

func testHookCapabilityClass(t *testing.T, typ ActionType, capability string) {
	t.Helper()
	for _, tc := range []struct {
		name    string
		class   policy.ActionClass
		deny    bool
		verdict policy.Verdict
		result  ResultCode
	}{
		{"allow", policy.ClassRead, false, policy.VerdictAllow, ResultSuccess},
		{"deny", policy.ClassRead, true, policy.VerdictDeny, ResultRefused},
		{"ask", policy.ClassWorkspaceMutation, false, policy.VerdictAsk, ResultAsk},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := hookPolicyRouter(t, tc.class, tc.deny)
			r := newRig(t)
			r.build(func(c *DispatcherConfig) {
				c.Router = router
				c.ActionCapabilities[typ] = capability
			})
			hook := HookConfig{ID: "class-test", Namespace: "jobs", ActionType: typ,
				ActionParams: map[string]string{"plugin": "mail", "tool": "send", "note": "daily"}}
			fire, err := r.d.dispatchHook(context.Background(), hook, "jobs", 1)
			if fire.ResultCode != tc.result || router.verdict != tc.verdict {
				t.Fatalf("fire=%+v verdict=%v err=%v", fire, router.verdict, err)
			}
			if router.action.Command != "" || !router.action.CommandLess || router.action.Origin != routing.OriginHook {
				t.Fatalf("action=%+v", router.action)
			}
			if tc.deny && router.trace.MatchedRule != policy.LayerStandingGrant.String() {
				t.Fatalf("explicit deny trace=%+v", router.trace)
			}
			wantRuns := 0
			if tc.verdict == policy.VerdictAllow {
				wantRuns = 1
			}
			if r.plugin.count()+r.note.count() != wantRuns || r.seam.count() != wantRuns {
				t.Fatal("only an allow may reach rehydration and the runner")
			}
		})
	}
}

// TestShellHookStillClassifiesCommand guards against shell bypasses.
func TestShellHookStillClassifiesCommand(t *testing.T) {
	for _, command := range []string{"cat notes.txt", "unrecognized-command", ""} {
		t.Run(command, func(t *testing.T) {
			router := hookPolicyRouter(t, policy.ClassRead, false)
			d := &Dispatcher{router: router, routeSubject: policy.Subject{Kind: policy.SubjectAgent, ID: "hooks"}, shellCapability: "hooks.shell"}
			out, ok := d.routeAction(context.Background(), Fire{Hook: HookConfig{ID: "shell", ActionType: ActionTypeShell}}, map[string]string{ShellCommandParam: command})
			if ok != (command == "cat notes.txt") {
				t.Fatalf("ok=%v outcome=%+v", ok, out)
			}
			if command == "" {
				if kind, _ := cascade.KindOf(out.err); kind != cascade.KindInvalidInput {
					t.Fatalf("err=%v", out.err)
				}
				return
			}
			if router.action.Command != command || router.action.CommandLess {
				t.Fatalf("action=%+v", router.action)
			}
		})
	}
}

// TestUnknownHookTypeNotCommandLess keeps future types on the classifier path.
func TestUnknownHookTypeNotCommandLess(t *testing.T) {
	router := hookPolicyRouter(t, policy.ClassRead, false)
	typ := ActionType("future-action")
	d := &Dispatcher{router: router, routeSubject: policy.Subject{Kind: policy.SubjectAgent, ID: "hooks"}, capabilities: map[ActionType]string{typ: "hooks.plugin_call"}}
	_, ok := d.routeAction(context.Background(), Fire{Hook: HookConfig{ID: "future", ActionType: typ}}, nil)
	if ok || router.action.Command != routeCommand(typ, nil) || router.action.CommandLess {
		t.Fatalf("ok=%v action=%+v", ok, router.action)
	}
}

// TestHookRouteSummaryCarriesNoParamValue guards summary and hash privacy.
func TestHookRouteSummaryCarriesNoParamValue(t *testing.T) {
	for _, typ := range []ActionType{ActionTypePluginCall, ActionTypeAgentNote} {
		router := hookPolicyRouter(t, policy.ClassRead, false)
		d := &Dispatcher{router: router, routeSubject: policy.Subject{Kind: policy.SubjectAgent, ID: "hooks"}, capabilities: map[ActionType]string{typ: "hooks.plugin_call"}}
		params := map[string]string{"plugin": "mail", "tool": "send", "note": "daily", "body": "private-payload-value"}
		_, ok := d.routeAction(context.Background(), Fire{Hook: HookConfig{ID: "summary", ActionType: typ}}, params)
		a := router.action
		if !ok || string(a.Params) != paramsHash(params) || strings.Contains(a.Summary+string(a.Params), params["body"]) {
			t.Fatalf("ok=%v action=%+v", ok, a)
		}
		target := "mail.send"
		if typ == ActionTypeAgentNote {
			target = "daily"
		}
		if !strings.Contains(a.Summary, string(typ)) || !strings.Contains(a.Summary, target) {
			t.Fatalf("summary=%q", a.Summary)
		}
	}
}
