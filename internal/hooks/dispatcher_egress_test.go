// Purpose: the egress-wiring tests for the dispatcher's outbound action
//
//	crossing. They drive the real production entry point (Dispatcher.Run
//	over a real bus) rather than the helper, so a change that removes the
//	firewall from that path turns them red.
//
// SPORT: EGRESS_CLASS_HOOK: ADD (P1-E08-W2-S16-T1).
package hooks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/hooks/egress"
)

// recordingDispatcher captures the params it was handed, which is what
// the firewall is supposed to have already rewritten.
type recordingDispatcher struct {
	params chan map[string]string
}

func (r *recordingDispatcher) DispatchPluginCall(_ context.Context, _ string, params map[string]string) error {
	select {
	case r.params <- params:
	default:
	}
	return nil
}

// TestDispatcherSubstitutesActionParamsOnTheRealPath publishes a real
// event on a real bus, lets the real dispatcher match and fire, and
// asserts the rehydration seam and the plugin seam never see the stored
// secret when the seam hands the tagged params on unchanged.
func TestDispatcherSubstitutesActionParamsOnTheRealPath(t *testing.T) {
	const secret = "correct-horse-battery-staple"
	r := newRig(t)
	engine := testEngine(t, map[string][]byte{"WIFI_PASSWORD": []byte(secret)})
	seam := &recordingDispatcher{params: make(chan map[string]string, 1)}
	r.register(HookConfig{Namespace: "deploy", Trigger: "deploy.finished", ActionType: ActionTypePluginCall,
		ActionParams: map[string]string{"credential": secret, "endpoint": "https://example.invalid"}})
	r.build(func(c *DispatcherConfig) {
		c.Egress = engine
		c.Runners[ActionTypePluginCall] = PluginCallRunner(seam)
	})
	r.start()
	if _, err := r.bus.Publish(context.Background(), "deploy", events.EventKind("deploy.finished"), "deploy-1", []byte("{}")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	select {
	case got := <-seam.params:
		assertSeamParams(t, got, secret)
	case <-time.After(5 * time.Second):
		t.Fatal("the dispatcher never reached the plugin seam")
	}
	if calls := r.seam.count(); calls != 1 {
		t.Fatalf("rehydration seam calls = %d, want 1", calls)
	}
	assertSeamParams(t, r.seam.calls[0], secret)
}

// assertSeamParams checks what the plugin seam was handed.
func assertSeamParams(t *testing.T, got map[string]string, secret string) {
	t.Helper()
	if strings.Contains(got["credential"], secret) {
		t.Fatalf("the plugin seam received the raw stored secret: %q", got["credential"])
	}
	if !strings.Contains(got["credential"], "<apikey>WIFI_PASSWORD</apikey>") {
		t.Fatalf("the credential param carries no vault reference: %q", got["credential"])
	}
	if got["endpoint"] != "https://example.invalid" {
		t.Fatalf("a parameter with no secret in it was altered: %q", got["endpoint"])
	}
}

// failingInterceptor refuses every call, standing in for a firewall that
// cannot run.
type failingInterceptor struct{}

func (failingInterceptor) Intercept(context.Context, egress.Capability, egress.SensitivityTier, []byte) ([]byte, error) {
	return nil, errors.New("firewall unavailable")
}

// TestDispatcherRefusesWhenTheFirewallCannotRun proves the fail-closed
// rule: when substitution cannot run, the action is refused with an empty
// params hash and neither the router, the seam nor a runner is reached.
func TestDispatcherRefusesWhenTheFirewallCannotRun(t *testing.T) {
	for _, actionType := range []ActionType{ActionTypePluginCall, ActionTypeAgentNote} {
		t.Run(string(actionType), func(t *testing.T) {
			r := newRig(t)
			r.build(func(c *DispatcherConfig) { c.Egress = failingInterceptor{} })
			hook := HookConfig{ID: "h", Namespace: "jobs", Trigger: "t", ActionType: actionType,
				ActionParams: map[string]string{"a": "b"}}
			fire, _ := r.d.dispatchHook(context.Background(), hook, "jobs", 1)
			if fire.ResultCode != ResultRefused || fire.ParamsHash != "" {
				t.Fatalf("fire = %+v, want refused with no params hash", fire)
			}
			if r.plugin.count()+r.note.count() != 0 || r.seam.count() != 0 || r.router.count() != 0 {
				t.Fatal("a stage after the firewall was reached despite its failure")
			}
		})
	}
}

// TestDispatcherRefusesUnreadableSubstitution proves the unparseable-output
// path: a firewall returning bytes this package cannot read refuses the
// action rather than falling back to the originals.
func TestDispatcherRefusesUnreadableSubstitution(t *testing.T) {
	token, err := egress.DefaultRegistry().Capability(egress.EgressClassHook)
	if err != nil {
		t.Fatalf("Capability(hook): %v", err)
	}
	params, err := interceptParams(context.Background(), garbageInterceptor{}, token, map[string]string{"a": "b"})
	if err == nil {
		t.Fatal("unreadable substituted params were accepted")
	}
	if params != nil {
		t.Fatalf("unreadable substituted params returned %v", params)
	}
	if _, err := interceptParams(context.Background(), nil, token, nil); err == nil {
		t.Fatal("a nil firewall was accepted")
	}
}

// garbageInterceptor returns bytes that are not the params shape.
type garbageInterceptor struct{}

func (garbageInterceptor) Intercept(context.Context, egress.Capability, egress.SensitivityTier, []byte) ([]byte, error) {
	return []byte("not json at all"), nil
}

// TestParamKeysSorted pins the diagnostic helper's ordering.
func TestParamKeysSorted(t *testing.T) {
	got := paramKeys(map[string]string{"z": "", "a": "", "m": ""})
	if strings.Join(got, ",") != "a,m,z" {
		t.Fatalf("paramKeys = %v, want a,m,z", got)
	}
}

// TestEncodeDecodeParamsRoundTrip covers the nil-map edges.
func TestEncodeDecodeParamsRoundTrip(t *testing.T) {
	encoded, err := encodeParams(nil)
	if err != nil {
		t.Fatalf("encodeParams(nil): %v", err)
	}
	out, err := decodeParams(encoded)
	if err != nil || out == nil || len(out) != 0 {
		t.Fatalf("decodeParams round trip = %v, %v", out, err)
	}
	if out, err := decodeParams([]byte("null")); err != nil || out == nil {
		t.Fatalf("decodeParams(null) = %v, %v; want an empty map", out, err)
	}
}
