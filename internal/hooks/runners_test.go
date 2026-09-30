// Purpose: the runner table and policy routing. Each type reaches its own
//
//	runner with the Fire identity, an entry smuggled past Register is
//	refused before any stage, and every action type is routed with
//	routing.OriginHook before the rehydration seam runs.
package hooks

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/events/routing"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// seamCall is one call a recording PluginDispatcher or NoteWriter saw.
type seamCall struct {
	hookID string
	params map[string]string
}

// recordingSeams implements PluginDispatcher and NoteWriter.
type recordingSeams struct {
	mu            sync.Mutex
	plugin, notes []seamCall
}

func (s *recordingSeams) DispatchPluginCall(_ context.Context, hookID string, params map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plugin = append(s.plugin, seamCall{hookID, cloneParams(params)})
	return nil
}

func (s *recordingSeams) WriteAgentNote(_ context.Context, hookID string, params map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notes = append(s.notes, seamCall{hookID, cloneParams(params)})
	return nil
}

// TestRunnerTableDispatchesByType proves plugin-call and agent-note reach
// the runners PluginCallRunner and AgentNoteRunner build, each with its
// own hook id and params, and that a runner's Fire carries the event
// identity and no raw params.
func TestRunnerTableDispatchesByType(t *testing.T) {
	r := newRig(t)
	seams := &recordingSeams{}
	r.register(HookConfig{ID: "p", Namespace: "jobs", Trigger: "k", ActionType: ActionTypePluginCall,
		ActionParams: map[string]string{"plugin": "x"}})
	r.register(HookConfig{ID: "n", Namespace: "jobs", Trigger: "k", ActionType: ActionTypeAgentNote,
		ActionParams: map[string]string{"note": "y"}})
	r.build(func(c *DispatcherConfig) {
		c.Runners[ActionTypePluginCall] = PluginCallRunner(seams)
		c.Runners[ActionTypeAgentNote] = AgentNoteRunner(seams)
	})
	sub := r.audit()
	r.start()
	r.publish("jobs", "k")
	nextFire(t, sub)
	nextFire(t, sub)
	seams.mu.Lock()
	defer seams.mu.Unlock()
	if len(seams.plugin) != 1 || seams.plugin[0].hookID != "p" || seams.plugin[0].params["plugin"] != "x" {
		t.Fatalf("plugin calls = %+v", seams.plugin)
	}
	if len(seams.notes) != 1 || seams.notes[0].hookID != "n" || seams.notes[0].params["note"] != "y" {
		t.Fatalf("note calls = %+v", seams.notes)
	}

	r2 := newRig(t)
	r2.build(nil)
	hook := HookConfig{ID: "q", Namespace: "jobs", Trigger: "k", ActionType: ActionTypeAgentNote,
		ActionParams: map[string]string{"note": "z"}}
	if fire, _ := r2.d.dispatchHook(context.Background(), hook, "jobs", 9); fire.ResultCode != ResultSuccess {
		t.Fatalf("fire = %+v", fire)
	}
	fires, params := r2.note.snapshot()
	got := fires[0]
	if len(fires) != 1 || got.Hook.ID != "q" || got.Namespace != "jobs" || got.EventSeq != 9 ||
		got.Chain.RootNamespace != "jobs" || got.Chain.RootSeq != 9 || got.Hook.ActionParams != nil || params[0]["note"] != "z" {
		t.Fatalf("runner fire = %+v params %v", fires, params)
	}
}

// TestUnrunnableTypeRefusedAtDispatch smuggles entries past Register into
// the registry map and proves each is refused before the firewall, the
// router, the seam or any runner, with one refused HookFire each.
func TestUnrunnableTypeRefusedAtDispatch(t *testing.T) {
	r := newRig(t)
	r.reg.hooks["pigeon"] = HookConfig{ID: "pigeon", Namespace: "jobs", Trigger: "k", ActionType: "carrier-pigeon"}
	r.reg.hooks["sh"] = HookConfig{ID: "sh", Namespace: "jobs", Trigger: "k", ActionType: ActionTypeShell,
		ActionParams: map[string]string{ShellCommandParam: "rm -rf /"}}
	r.build(nil)
	sub := r.audit()
	r.start()
	r.publish("jobs", "k")
	for i := 0; i < 2; i++ {
		fire, _ := nextFire(t, sub)
		if fire.ResultCode != ResultRefused || !strings.Contains(fire.ErrMsg, HookActionNotPermittedCode) || fire.ParamsHash != "" {
			t.Fatalf("fire = %+v, want refused before egress", fire)
		}
	}
	if r.router.count()+r.seam.count()+r.plugin.count()+r.note.count() != 0 {
		t.Fatal("a smuggled entry reached a stage past the runnable check")
	}
	if fires := r.d.Fires(); len(fires) != 2 {
		t.Fatalf("fires ring holds %d records, want 2", len(fires))
	}
	if err := r.d.Swap(r.reg); err == nil {
		t.Fatal("Swap accepted a registry holding unrunnable hooks")
	}
}

// TestEveryActionRoutedWithOriginHook proves plugin-call and agent-note
// each call RouteAction once, with OriginHook, the type's capability, the
// tagged params hash and command, before the rehydration seam runs.
func TestEveryActionRoutedWithOriginHook(t *testing.T) {
	r := vaultRig(t)
	order := &callOrder{}
	r.router.order, r.seam.order = order, order
	r.build(nil)
	hooks := []HookConfig{
		{ID: "p", Namespace: "jobs", ActionType: ActionTypePluginCall,
			ActionParams: map[string]string{"plugin": "mail", "tool": "send", "key": vaultSecret}},
		{ID: "n", Namespace: "jobs", ActionType: ActionTypeAgentNote, ActionParams: map[string]string{"note": "daily"}},
	}
	wantCap := map[string]string{"p": "hooks.plugin", "n": "hooks.note"}
	wantCmd := map[string]string{"p": "plugin-call mail.send", "n": "agent-note daily"}
	for i, h := range hooks {
		if fire, _ := r.d.dispatchHook(context.Background(), h, "jobs", uint64(i+1)); fire.ResultCode != ResultSuccess {
			t.Fatalf("fire = %+v", fire)
		}
	}
	actions := r.router.actions()
	if len(actions) != 2 {
		t.Fatalf("router saw %d actions, want 2", len(actions))
	}
	for i, a := range actions {
		tagged := r.seam.calls[i]
		if a.Origin != routing.OriginHook || a.Capability != wantCap[a.Ref] || a.Command != wantCmd[a.Ref] ||
			a.Verb != string(EventKindHookFire) || string(a.Params) != paramsHash(tagged) {
			t.Fatalf("routed action %d = %+v", i, a)
		}
		if strings.Contains(string(a.Params)+a.Command+a.Summary, vaultSecret) {
			t.Fatal("the routed action carries the raw secret")
		}
	}
	if string(actions[0].Params) == paramsHash(hooks[0].ActionParams) {
		t.Fatal("the routed params hash is the raw config hash, not the tagged one")
	}
	if got := strings.Join(order.names, ","); got != "route,seam,route,seam" {
		t.Fatalf("stage order = %s, want routing before the seam each time", got)
	}
}

// TestRouterDenyRefusesPluginCall proves a deny refuses with the router's
// typed error and never reaches the seam or the runner.
func TestRouterDenyRefusesPluginCall(t *testing.T) {
	r := newRig(t)
	r.router.verdict = policy.VerdictDeny
	r.router.err = cascade.New(cascade.KindPolicyDenied, "routing: refused")
	r.build(nil)
	hook := HookConfig{ID: "p", Namespace: "jobs", ActionType: ActionTypePluginCall, ActionParams: map[string]string{"plugin": "x"}}
	fire, err := r.d.dispatchHook(context.Background(), hook, "jobs", 1)
	if fire.ResultCode != ResultRefused || !strings.Contains(err.Error(), "routing: refused") {
		t.Fatalf("fire = %+v err = %v", fire, err)
	}
	if r.router.count() != 1 || r.seam.count() != 0 || r.plugin.count() != 0 {
		t.Fatalf("counts router=%d seam=%d runner=%d, want 1/0/0", r.router.count(), r.seam.count(), r.plugin.count())
	}
}

// TestRouterAskRecordsAskNoRun proves an ask is recorded as ask and runs
// nothing.
func TestRouterAskRecordsAskNoRun(t *testing.T) {
	r := newRig(t)
	r.router.verdict = policy.VerdictAsk
	r.build(nil)
	hook := HookConfig{ID: "n", Namespace: "jobs", ActionType: ActionTypeAgentNote}
	fire, _ := r.d.dispatchHook(context.Background(), hook, "jobs", 1)
	if fire.ResultCode != ResultAsk {
		t.Fatalf("fire = %+v, want ask", fire)
	}
	if r.seam.count() != 0 || r.note.count() != 0 {
		t.Fatal("an asked action reached the seam or the runner")
	}
	if fires := r.d.Fires(); len(fires) != 1 || fires[0].ResultCode != ResultAsk {
		t.Fatalf("fires ring = %+v", fires)
	}
}
