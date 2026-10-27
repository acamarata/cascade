//go:build !windows

package main

// Purpose (this file): the presence registration over the PRODUCTION
//   composition (composeDaemon, an enrolled in-memory test custody under a
//   fresh HOME). The order is proven through the real registry: an attested
//   elevated call advances the tracker, and calls that never carried a verified
//   witness do not. No host keystore, keychain or operator HOME is touched.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

// capturePresenceTrackers swaps newPresenceTracker for a wrapper that records
// every tracker built, restored when the test ends.
func capturePresenceTrackers(t *testing.T) func() []*daemon.PresenceTracker {
	t.Helper()
	orig := newPresenceTracker
	var mu sync.Mutex
	var built []*daemon.PresenceTracker
	newPresenceTracker = func(c runtime.Clock) *daemon.PresenceTracker {
		tr := orig(c)
		mu.Lock()
		built = append(built, tr)
		mu.Unlock()
		return tr
	}
	t.Cleanup(func() { newPresenceTracker = orig })
	return func() []*daemon.PresenceTracker {
		mu.Lock()
		defer mu.Unlock()
		return append([]*daemon.PresenceTracker(nil), built...)
	}
}

// presenceCanaries registers two elevated canaries on the composed registry:
// one that succeeds and one that fails, and counts how often each ran.
func presenceCanaries(d composedElevated) map[string]int {
	ran := map[string]int{}
	register := func(method string, fail error) {
		d.Registry.Register(method, func(context.Context, json.RawMessage) (any, error) {
			ran[method]++
			return map[string]string{"ok": "yes"}, fail
		})
	}
	register("vault.rotate", nil)
	register("vault.get", errors.New("canary failure"))
	return ran
}

func TestPresenceMiddlewareInnerToElevation(t *testing.T) {
	built := capturePresenceTrackers(t)
	d := composeElevated(t, true)
	trackers := built()
	if len(trackers) != 1 {
		t.Fatalf("composeDaemon built %d presence trackers, want exactly 1", len(trackers))
	}
	tracker := trackers[0]
	if !tracker.LastActivity().IsZero() {
		t.Fatalf("a fresh daemon reports presence at %v", tracker.LastActivity())
	}
	ran := presenceCanaries(d)

	// An attested elevated call that succeeds advances presence to about now.
	before := time.Now().Add(-time.Second)
	if err := d.Call(context.Background(), "vault.rotate", map[string]string{"from": "x"}, nil); err != nil {
		t.Fatalf("attested call: %v", err)
	}
	counted := tracker.LastActivity()
	if counted.Before(before) || counted.After(time.Now().Add(time.Second)) || ran["vault.rotate"] != 1 {
		t.Fatalf("LastActivity = %v, handler runs = %d after one attested call, want about now and 1", counted, ran["vault.rotate"])
	}

	// Calls that carry no verified success leave it where it was.
	ctx := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	dispatch := func(method string) *rpc.ErrorObject {
		_, eo := d.Registry.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: method, Params: json.RawMessage(`{}`)})
		return eo
	}
	if eo := dispatch("vault.rotate"); eo == nil || ran["vault.rotate"] != 1 {
		t.Fatalf("an unattested elevated call reached the handler: eo=%v runs=%d", eo, ran["vault.rotate"])
	}
	d.Registry.Register("chat.append_turn", func(context.Context, json.RawMessage) (any, error) { return "ok", nil })
	for _, method := range []string{"status.get", "chat.append_turn"} {
		if eo := dispatch(method); eo != nil {
			t.Fatalf("%s did not succeed, so the no-witness case proves nothing: %v", method, eo)
		}
	}
	if err := d.Call(context.Background(), "vault.get", map[string]string{"k": "x"}, nil); err == nil || ran["vault.get"] != 1 {
		t.Fatalf("the failing canary: err=%v runs=%d, want one failing run", err, ran["vault.get"])
	}
	if got := tracker.LastActivity(); !got.Equal(counted) {
		t.Fatalf("LastActivity moved from %v to %v on calls that carried no verified success", counted, got)
	}
}

// wirePresence keeps a tracker the wiring already carries and builds one
// otherwise; a wiring with no clock yields a tracker that records nothing.
func TestWirePresenceKeepsAnExistingTracker(t *testing.T) {
	built := capturePresenceTrackers(t)
	own := daemon.NewPresenceTracker(runtime.NewSystemClock())
	w := &daemonWiring{Registry: rpc.NewRegistry(), Deps: daemonRuntime{Presence: own}}
	if err := wirePresence(w); err != nil {
		t.Fatal(err)
	}
	if w.Deps.Presence != own || len(built()) != 0 {
		t.Fatal("wirePresence replaced a tracker the wiring already carried")
	}
	bare := &daemonWiring{Registry: rpc.NewRegistry()}
	if err := wirePresence(bare); err != nil {
		t.Fatal(err)
	}
	if bare.Deps.Presence == nil || len(built()) != 1 {
		t.Fatalf("wirePresence built %d trackers for a bare wiring, want 1", len(built()))
	}
	if !bare.Deps.Presence.LastActivity().IsZero() {
		t.Fatal("a tracker with no clock reports activity")
	}
}
