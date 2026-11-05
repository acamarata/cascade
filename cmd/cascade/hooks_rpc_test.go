// Purpose: hooks.list and hooks.fires carry no param value, rehydrated
// value or plaintext hash, keep the params hash, and degrade to
// KindUnavailable without a Dispatcher.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/events/routing"
	"github.com/acamarata/cascade/internal/hooks"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The canaries. vaultCanary is stored in the test vault, so the real
// egress engine tags it; plainCanary is what the seam rehydrates the tag
// to; literalCanary is a configured value the vault does not hold.
const (
	vaultCanaryName = "HOOKS_RPC_CANARY"
	vaultCanary     = "quartz-lantern" + "-meadow-plover"
	plainCanary     = "rehydrated-" + "violet-harbor"
	literalCanary   = "literal-" + "copper-thistle"
)

// hooksTestVault is an in-memory egress vault.
type hooksTestVault map[string][]byte

func (v hooksTestVault) List(context.Context) ([]string, error) {
	out := make([]string, 0, len(v))
	for k := range v {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

func (v hooksTestVault) Get(_ context.Context, name string) ([]byte, error) {
	val, ok := v[name]
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound, "test vault: %q", name)
	}
	return val, nil
}

// allowRouter allows every action.
type allowRouter struct{}

func (allowRouter) RouteAction(context.Context, routing.Action) (policy.Verdict, policy.Trace, error) {
	return policy.VerdictAllow, policy.Trace{}, nil
}

// echoFailRunner fails every fire, quoting the params it was handed.
type echoFailRunner struct{}

func (echoFailRunner) RunAction(_ context.Context, _ hooks.Fire, params map[string]string) error {
	return fmt.Errorf("runner refused input %v", params)
}

// rehydrateTag puts plainCanary where the egress tag of the vault canary is.
func rehydrateTag(_ context.Context, _ hooks.Fire, tagged map[string]string) (map[string]string, func(), error) {
	tag := "<apikey>" + vaultCanaryName + "</apikey>"
	out := make(map[string]string, len(tagged))
	for k, v := range tagged {
		out[k] = strings.ReplaceAll(v, tag, plainCanary)
	}
	return out, func() {}, nil
}

// hooksRPCHome points HOME, USERPROFILE and CASCADE_HOME at a temp dir.
func hooksRPCHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, k := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(k, dir)
	}
}

// hooksRig is a real Dispatcher over a memory bus and the real egress
// engine, with the failing echo runner for plugin-call.
type hooksRig struct {
	bus *events.Bus
	d   *hooks.Dispatcher
}

func newHooksRig(t *testing.T, cfgs ...hooks.HookConfig) *hooksRig {
	t.Helper()
	hooksRPCHome(t)
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })
	reg := hooks.NewRegistry()
	for _, c := range cfgs {
		if _, err := reg.Register(c); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	engine, err := egress.NewEngine(egress.DefaultRegistry(), hooksTestVault{vaultCanaryName: []byte(vaultCanary)}, detector)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	tok, err := egress.DefaultRegistry().Capability(egress.EgressClassHook)
	if err != nil {
		t.Fatalf("Capability: %v", err)
	}
	d, err := hooks.NewDispatcher(hooks.DispatcherConfig{
		Registry: reg, Bus: bus, Clock: clock, Egress: engine, EgressToken: tok,
		Runners:            map[hooks.ActionType]hooks.ActionRunner{hooks.ActionTypePluginCall: echoFailRunner{}},
		ActionCapabilities: map[hooks.ActionType]string{hooks.ActionTypePluginCall: "hooks.plugin"},
		Rehydrate:          rehydrateTag, Router: allowRouter{},
		RouteSubject: policy.Subject{Kind: policy.SubjectAgent, ID: "hooks"}, State: storetest.NewMemStore(),
		ActionTimeout: 3 * time.Second, AuditNamespace: hooks.AuditNamespace, CursorPrefix: "hooks:", SubscribeBuffer: 64,
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	return &hooksRig{bus: bus, d: d}
}

// fireOnce runs the dispatcher and publishes ns/kind until a fire is
// recorded (the subscription starts at head, so an early publish is skipped).
func (r *hooksRig) fireOnce(t *testing.T, ns, kind string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = r.d.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(10 * time.Second)
	for len(r.d.Fires()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no hook fire recorded")
		}
		if _, err := r.bus.Publish(context.Background(), ns, events.EventKind(kind), "test", nil); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		for i := 0; i < 20 && len(r.d.Fires()) == 0; i++ {
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// ownerRegistry returns a registry with access checks on and the hooks
// verbs mounted over d, plus the owner context.
func ownerRegistry(d *hooks.Dispatcher) (*rpc.Registry, context.Context) {
	reg := rpc.NewRegistry()
	reg.SetAccessPolicy(func(string) (principal.Principal, error) {
		return principal.Principal{}, cascade.New(cascade.KindPermissionDenied, "caller principal not recognized")
	})
	registerHooksRPC(reg, d, nil)
	return reg, principal.WithCaller(context.Background(), principal.Caller{})
}

// callWire dispatches method as the owner and returns the JSON reply.
func callWire(ctx context.Context, t *testing.T, reg *rpc.Registry, method string) string {
	t.Helper()
	res, eo := reg.Dispatch(ctx, &rpc.Request{Method: method, Params: json.RawMessage(`{}`)})
	if eo != nil {
		t.Fatalf("%s: %+v", method, eo)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal %s: %v", method, err)
	}
	return string(raw)
}

func hooksRPCDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// assertAbsent fails when wire carries any of needles.
func assertAbsent(t *testing.T, label, wire string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if strings.Contains(wire, n) {
			t.Fatalf("%s carries a forbidden value (%d bytes): %s", label, len(n), wire)
		}
	}
}

// TestHooksListOmitsConfiguredParamValues: a hook with a literal canary
// param lists its keys and params_hash only.
func TestHooksListOmitsConfiguredParamValues(t *testing.T) {
	params := map[string]string{"note": literalCanary, "endpoint": "https://example.invalid"}
	r := newHooksRig(t, hooks.HookConfig{ID: "lit", Namespace: "jobs", Trigger: "k",
		ActionType: hooks.ActionTypePluginCall, ActionParams: params})
	reg, ctx := ownerRegistry(r.d)
	wire := callWire(ctx, t, reg, hooksListMethod)
	assertAbsent(t, "hooks.list", wire, literalCanary, hooksRPCDigest(literalCanary), "example.invalid")
	var got hooksListResult
	if err := json.Unmarshal([]byte(wire), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Hooks) != 1 || got.Stats.Registered != 1 || got.Stats.LastFire != nil {
		t.Fatalf("hooks.list = %s", wire)
	}
	h := got.Hooks[0]
	if h.ID != "lit" || h.Namespace != "jobs" || h.Trigger != "k" || h.ActionType != hooks.ActionTypePluginCall ||
		strings.Join(h.ParamKeys, ",") != "endpoint,note" || h.ParamsHash != configParamsHash(params) || len(h.ParamsHash) != 64 {
		t.Fatalf("hook row = %+v", h)
	}
	anon := principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{ID: "p", Kind: principal.KindSession}})
	if _, eo := reg.Dispatch(anon, &rpc.Request{Method: hooksListMethod}); eo == nil {
		t.Fatal("a principal reached the owner-only hooks.list")
	}
}

// vaultHook carries the vault canary, so the engine tags it and the seam
// rehydrates it before the failing runner echoes it.
var vaultHook = hooks.HookConfig{ID: "cred", Namespace: "jobs", Trigger: "k", ActionType: hooks.ActionTypePluginCall,
	ActionParams: map[string]string{"credential": vaultCanary, "endpoint": "https://example.invalid"}}

// forbidden is every value neither verb may carry after a vaultHook fire.
func forbidden() []string {
	return []string{vaultCanary, plainCanary, hooksRPCDigest(vaultCanary), hooksRPCDigest(plainCanary),
		hooksRPCDigest("https://example.invalid")}
}

// TestHooksListRedactsParams: after a failing fire, hooks.list carries no
// vault value, rehydrated plaintext or plaintext hash, and keeps the hash.
func TestHooksListRedactsParams(t *testing.T) {
	r := newHooksRig(t, vaultHook)
	r.fireOnce(t, "jobs", "k")
	reg, ctx := ownerRegistry(r.d)
	wire := callWire(ctx, t, reg, hooksListMethod)
	assertAbsent(t, "hooks.list", wire, forbidden()...)
	var got hooksListResult
	if err := json.Unmarshal([]byte(wire), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Hooks) != 1 || len(got.Hooks[0].ParamsHash) != 64 || got.Stats.LastFire == nil {
		t.Fatalf("hooks.list = %s", wire)
	}
}

// TestHooksFiresNeverCarriesParams: the recorded failure reaches
// hooks.fires with its params_hash and ErrMsg, and none of the values.
func TestHooksFiresNeverCarriesParams(t *testing.T) {
	r := newHooksRig(t, vaultHook)
	r.fireOnce(t, "jobs", "k")
	reg, ctx := ownerRegistry(r.d)
	wire := callWire(ctx, t, reg, hooksFiresMethod)
	assertAbsent(t, "hooks.fires", wire, forbidden()...)
	assertAbsent(t, "hooks.fires", wire, `"action_params"`, `"params"`)
	var got hooksFiresResult
	if err := json.Unmarshal([]byte(wire), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Fires) == 0 {
		t.Fatalf("hooks.fires = %s", wire)
	}
	f := got.Fires[0]
	if f.HookID != "cred" || f.ResultCode != hooks.ResultError || f.ErrMsg == "" || len(f.ParamsHash) != 64 {
		t.Fatalf("fire = %+v", f)
	}
}

// TestHooksRPCDegradedAnswersUnavailable: a nil Dispatcher answers
// errHooksDegraded from both verbs, by identity and message.
func TestHooksRPCDegradedAnswersUnavailable(t *testing.T) {
	hooksRPCHome(t)
	h := hooksRPC{}
	for name, call := range map[string]rpc.HandlerFunc{hooksListMethod: h.list, hooksFiresMethod: h.fires} {
		_, err := call(context.Background(), json.RawMessage(`{}`))
		var ce *cascade.Error
		if !errors.Is(err, errHooksDegraded) || !errors.As(err, &ce) ||
			ce.Kind != cascade.KindUnavailable || ce.Msg != "hooks subsystem degraded" {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	reg, ctx := ownerRegistry(nil)
	for _, m := range []string{hooksListMethod, hooksFiresMethod} {
		_, eo := reg.Dispatch(ctx, &rpc.Request{Method: m})
		if eo == nil || !strings.Contains(eo.Message, "hooks subsystem degraded") {
			t.Fatalf("%s over the wire: %+v", m, eo)
		}
	}
}
