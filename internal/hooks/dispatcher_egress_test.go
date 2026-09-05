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
// asserts the plugin seam never sees the stored secret.
func TestDispatcherSubstitutesActionParamsOnTheRealPath(t *testing.T) {
	const secret = "correct-horse-battery-staple"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bus, clock := testBus(t)
	engine := testEngine(t, map[string][]byte{"WIFI_PASSWORD": []byte(secret)})
	token, err := egress.DefaultRegistry().Capability(egress.EgressClassHook)
	if err != nil {
		t.Fatalf("Capability(hook): %v", err)
	}
	seam := &recordingDispatcher{params: make(chan map[string]string, 1)}

	reg := NewRegistry()
	hook := HookConfig{
		Trigger:      "deploy.finished",
		ActionType:   ActionTypePluginCall,
		ActionParams: map[string]string{"credential": secret, "endpoint": "https://example.invalid"},
	}
	if _, err := reg.Register(hook); err != nil {
		t.Fatalf("Register: %v", err)
	}

	d, err := NewDispatcher(DispatcherConfig{
		Registry: reg, Bus: bus, Clock: clock,
		Egress: engine, EgressToken: token,
		PluginDispatcher: seam, NoteWriter: &fakeNoteWriter{},
		ActionTimeout: 3 * time.Second, TriggerNamespace: "triggers",
		AuditNamespace: "audit", CursorName: "dispatcher", SubscribeBuffer: 8,
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	go func() { _ = d.Run(ctx) }()

	if _, err := bus.Publish(ctx, "triggers", events.EventKind("deploy.finished"), "deploy-1", []byte("{}")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case got := <-seam.params:
		assertSeamParams(t, got, secret)
	case <-ctx.Done():
		t.Fatal("the dispatcher never reached the plugin seam")
	}
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
// rule at the dispatcher: when substitution cannot run, the action is
// refused and the seam is never called at all.
func TestDispatcherRefusesWhenTheFirewallCannotRun(t *testing.T) {
	for _, tc := range []struct {
		name       string
		actionType ActionType
	}{{"plugin-call", ActionTypePluginCall}, {"agent-note", ActionTypeAgentNote}} {
		t.Run(tc.name, func(t *testing.T) {
			bus, clock := testBus(t)
			token, err := egress.DefaultRegistry().Capability(egress.EgressClassHook)
			if err != nil {
				t.Fatalf("Capability(hook): %v", err)
			}
			pd := &fakePluginDispatcher{}
			nw := &fakeNoteWriter{}
			d, err := NewDispatcher(DispatcherConfig{
				Registry: NewRegistry(), Bus: bus, Clock: clock,
				Egress: failingInterceptor{}, EgressToken: token,
				PluginDispatcher: pd, NoteWriter: nw,
				ActionTimeout: time.Second, TriggerNamespace: "triggers",
				AuditNamespace: "audit", CursorName: "dispatcher", SubscribeBuffer: 8,
			})
			if err != nil {
				t.Fatalf("NewDispatcher: %v", err)
			}
			outcome := d.runAction(context.Background(), HookConfig{
				ID: "h", ActionType: tc.actionType, ActionParams: map[string]string{"a": "b"},
			})
			if outcome.result != ResultRefused {
				t.Fatalf("result = %q, want %q", outcome.result, ResultRefused)
			}
			if len(pd.calls) != 0 || len(nw.calls) != 0 {
				t.Fatal("the action seam was reached despite a firewall failure")
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
