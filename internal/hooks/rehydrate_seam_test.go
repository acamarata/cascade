// Purpose: the rehydration seam. It receives exactly the post-egress
//
//	params, the runner receives exactly its output, it runs per fire,
//	its failure refuses the fire, its zero runs exactly once on every
//	path, and no plaintext it produced reaches a HookFire, the ring, the
//	stored audit event or the returned error.
package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/cascade"
)

// vaultSecret is the stored credential; vaultTag is what the real egress
// engine substitutes for it.
const (
	vaultSecret = "correct-horse" + "-battery-staple"
	vaultTag    = "<apikey>WIFI_PASSWORD</apikey>"
)

// vaultRig is a rig whose firewall is the real engine over a vault holding
// vaultSecret, so tagged params differ from the configured ones.
func vaultRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t)
	r.egress = testEngine(t, map[string][]byte{"WIFI_PASSWORD": []byte(vaultSecret)})
	return r
}

// untag is a seam transform that puts plain back where the tag is.
func untag(plain string) func(map[string]string) map[string]string {
	return func(tagged map[string]string) map[string]string {
		out := make(map[string]string, len(tagged))
		for k, v := range tagged {
			out[k] = strings.ReplaceAll(v, vaultTag, plain)
		}
		return out
	}
}

var credHook = HookConfig{ID: "cred", Namespace: "jobs", Trigger: "k", ActionType: ActionTypePluginCall,
	ActionParams: map[string]string{"credential": vaultSecret, "endpoint": "https://example.invalid"}}

// TestActionDispatchRehydratesTaggedParams proves the seam gets exactly the
// firewall's output, the runner gets exactly the seam's output, and a
// second fire asks the seam again (nothing cached from load or the first
// fire).
func TestActionDispatchRehydratesTaggedParams(t *testing.T) {
	r := vaultRig(t)
	r.seam.transform = untag("first-plain")
	r.build(nil)
	tok, _ := egress.DefaultRegistry().Capability(egress.EgressClassHook)
	want, err := interceptParams(context.Background(), r.egress, tok, credHook.ActionParams)
	if err != nil || want["credential"] != vaultTag {
		t.Fatalf("firewall output = %v, %v", want, err)
	}
	_, _ = r.d.dispatchHook(context.Background(), credHook, "jobs", 1)
	r.seam.mu.Lock()
	r.seam.transform = untag("second-plain")
	r.seam.mu.Unlock()
	_, _ = r.d.dispatchHook(context.Background(), credHook, "jobs", 2)

	if r.seam.count() != 2 || !maps.Equal(r.seam.calls[0], want) || !maps.Equal(r.seam.calls[1], want) {
		t.Fatalf("seam received %v, want exactly the post-egress params %v twice", r.seam.calls, want)
	}
	_, params := r.plugin.snapshot()
	if len(params) != 2 || !maps.Equal(params[0], untag("first-plain")(want)) || !maps.Equal(params[1], untag("second-plain")(want)) {
		t.Fatalf("runner received %v, want the seam's output per fire", params)
	}
}

// TestRehydrateErrorRefusesAction proves a seam failure records rehydrate
// with a fixed text, never calls the runner, still zeroes, and leaks none
// of the partial plaintext it returned or quoted.
func TestRehydrateErrorRefusesAction(t *testing.T) {
	r := vaultRig(t)
	r.seam.transform = untag("partial-" + "plaintext")
	r.seam.err = errors.New("vault locked after reading partial-plaintext")
	r.build(nil)
	fire, err := r.d.dispatchHook(context.Background(), credHook, "jobs", 1)
	if fire.ResultCode != ResultRehydrate || r.plugin.count() != 0 || r.seam.zeros.Load() != 1 {
		t.Fatalf("fire = %+v runner=%d zeros=%d", fire, r.plugin.count(), r.seam.zeros.Load())
	}
	if strings.Contains(fire.ErrMsg+err.Error(), "partial-plaintext") || fire.ErrMsg != "internal: hooks: rehydration seam failed" {
		t.Fatalf("partial plaintext leaked or fixed text missing: %q / %v", fire.ErrMsg, err)
	}
}

// TestRehydratedBufferZeroedAfterDispatch proves zero runs exactly once,
// after the runner, on success, runner error, panic and timeout.
func TestRehydratedBufferZeroedAfterDispatch(t *testing.T) {
	cases := map[string]struct {
		setup func(*fakeRunner)
		want  ResultCode
	}{
		"success": {func(*fakeRunner) {}, ResultSuccess},
		"error":   {func(f *fakeRunner) { f.err = errors.New("boom") }, ResultError},
		"panic":   {func(f *fakeRunner) { f.panics = true }, ResultPanic},
		"timeout": {func(f *fakeRunner) { f.block = make(chan struct{}) }, ResultTimeout},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			tc.setup(r.plugin)
			var zerosAtRun int32 = -1
			if r.plugin.block == nil && !r.plugin.panics && r.plugin.err == nil {
				r.plugin.onRun = func(context.Context, Fire, map[string]string) error {
					zerosAtRun = r.seam.zeros.Load()
					return nil
				}
			}
			r.build(func(c *DispatcherConfig) { c.ActionTimeout = 50 * time.Millisecond })
			fire, _ := r.d.dispatchHook(context.Background(), credHook, "jobs", 1)
			if fire.ResultCode != tc.want || r.seam.zeros.Load() != 1 {
				t.Fatalf("result %q zeros %d, want %q and exactly 1", fire.ResultCode, r.seam.zeros.Load(), tc.want)
			}
			if name == "success" && zerosAtRun != 0 {
				t.Fatalf("zero ran before the runner (%d)", zerosAtRun)
			}
		})
	}
}

// TestHookFireAuditCarriesTaggedHashOnly proves the stored audit event and
// the ring carry the tagged params hash, never the raw config hash or the
// raw value.
func TestHookFireAuditCarriesTaggedHashOnly(t *testing.T) {
	r := vaultRig(t)
	r.register(credHook)
	r.build(nil)
	sub := r.audit()
	r.start()
	r.publish("jobs", "k")
	fire, ev := nextFire(t, sub)
	tagged := r.seam.calls[0]
	rawHash := paramsHash(credHook.ActionParams)
	if fire.ParamsHash != paramsHash(tagged) || fire.ParamsHash == rawHash {
		t.Fatalf("ParamsHash = %q, want the tagged hash %q", fire.ParamsHash, paramsHash(tagged))
	}
	if strings.Contains(string(ev.Payload), rawHash) || strings.Contains(string(ev.Payload), vaultSecret) {
		t.Fatalf("stored audit payload carries raw material: %s", ev.Payload)
	}
	if ring := r.d.Fires(); len(ring) != 1 || ring[0].ParamsHash != fire.ParamsHash {
		t.Fatalf("ring = %+v", ring)
	}
}

// TestNewDispatcherRequiresRehydrate proves there is no default seam.
func TestNewDispatcherRequiresRehydrate(t *testing.T) {
	r := newRig(t)
	cfg := r.config()
	cfg.Rehydrate = nil
	d, err := NewDispatcher(cfg)
	var ce *cascade.Error
	if d != nil || !errors.As(err, &ce) || ce.Kind != cascade.KindInvalidInput ||
		err.Error() != "invalid-input: hooks: dispatcher: Rehydrate is required" {
		t.Fatalf("NewDispatcher(no seam) = %v, %v", d, err)
	}
}

// TestHookFireErrMsgRedactsRehydratedValue proves a runner error echoing
// the rehydrated plaintext is scrubbed to the tag in the stored event, the
// ring, Stats and the returned error.
func TestHookFireErrMsgRedactsRehydratedValue(t *testing.T) {
	const plain = "hunter2-" + "rehydrated"
	r := vaultRig(t)
	r.seam.transform = untag(plain)
	r.plugin.err = errors.New("auth failed for " + plain + " at endpoint")
	r.build(nil)
	sub := r.audit()
	fire, err := r.d.dispatchHook(context.Background(), credHook, "jobs", 1)
	_, ev := nextFire(t, sub)
	stats, _ := json.Marshal(r.d.Stats())
	ring, _ := json.Marshal(r.d.Fires())
	for name, text := range map[string]string{"payload": string(ev.Payload), "ring": string(ring),
		"stats": string(stats), "error": err.Error(), "fire": fire.ErrMsg} {
		if strings.Contains(text, plain) {
			t.Fatalf("%s carries the rehydrated plaintext: %s", name, text)
		}
	}
	if !strings.Contains(fire.ErrMsg, "auth failed for "+vaultTag) || !strings.Contains(string(ev.Payload), "WIFI_PASSWORD") {
		t.Fatalf("tag missing: %q", fire.ErrMsg)
	}
	if k, _ := cascade.KindOf(err); k != cascade.KindInternal {
		t.Fatalf("scrubbed error kind = %v", k)
	}
}

// TestSeamFailureTextNeverRecorded proves a seam that panics or errors
// while quoting plaintext it never returned (so nothing pairs it to a tag)
// records a fixed text: the plaintext is absent from the HookFire, the
// stored audit payload, the ring and the returned error.
func TestSeamFailureTextNeverRecorded(t *testing.T) {
	const plain = "sk-" + "probeplain0123456789"
	cases := map[string]struct {
		seam   RehydrateFunc
		code   ResultCode
		errMsg string
	}{
		"panic": {func(context.Context, Fire, map[string]string) (map[string]string, func(), error) {
			panic("decode failed near " + plain)
		}, ResultPanic, "internal: hooks: panic in action (string)"},
		"error": {func(context.Context, Fire, map[string]string) (map[string]string, func(), error) {
			return nil, nil, errors.New("vault refused " + plain)
		}, ResultRehydrate, "internal: hooks: rehydration seam failed"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := vaultRig(t)
			r.build(func(c *DispatcherConfig) { c.Rehydrate = tc.seam })
			sub := r.audit()
			fire, err := r.d.dispatchHook(context.Background(), credHook, "jobs", 1)
			_, ev := nextFire(t, sub)
			ring, _ := json.Marshal(r.d.Fires())
			if err == nil || fire.ResultCode != tc.code || fire.ErrMsg != tc.errMsg || err.Error() != tc.errMsg {
				t.Fatalf("fire %q %q, err %v; want %q %q", fire.ResultCode, fire.ErrMsg, err, tc.code, tc.errMsg)
			}
			for where, text := range map[string]string{"payload": string(ev.Payload), "ring": string(ring),
				"error": err.Error(), "fire": fire.ErrMsg} {
				if strings.Contains(text, "probeplain") {
					t.Fatalf("%s carries the seam's plaintext: %s", where, text)
				}
			}
		})
	}
}
