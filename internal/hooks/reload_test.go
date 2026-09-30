// Purpose: hot reload. An invalid section keeps the previous hooks firing
//
//	and publishes a rejection without params; a valid one swaps; a reload
//	announced after WatchReload and before Run is still applied; a Swap
//	that fails part-way keeps the previous set and never replays.
//
// Constraints: ReloadWatch handles events in order, so a trailing invalid
//
//	reload is the barrier that proves every earlier reload was applied.
package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

const (
	reloadNS   = "config"
	reloadKind = events.EventKind("config.reloaded")
	leakValue  = "sekrit-" + "param-value"
)

// invalidSection is refused: its entry carries an unknown key, next to a
// param value that must never appear in the rejection.
var invalidSection = []any{map[string]any{"namespace": "deploy", "trigger": "done", "action_type": "plugin-call",
	"action_params": map[string]any{"token": leakValue}, "bogus": leakValue}}

// validSection loads one hook on namespace deploy.
var validSection = []any{map[string]any{"id": "deployed", "namespace": "deploy", "trigger": "done",
	"action_type": "agent-note"}}

// sequenceSource returns each section in turn, then the last forever.
func sequenceSource(sections ...any) func() (any, error) {
	var mu sync.Mutex
	i := 0
	return func() (any, error) {
		mu.Lock()
		defer mu.Unlock()
		s := sections[i]
		if i < len(sections)-1 {
			i++
		}
		return s, nil
	}
}

// reloadRig is a running dispatcher with hook "old" on jobs/k, a reload
// watch, and a subscription observing the reload namespace.
func reloadRig(t *testing.T) (*rig, *ReloadWatch, *events.Subscription) {
	t.Helper()
	return reloadRigOn(t, newRig(t))
}

// reloadRigOn is reloadRig over a caller-built rig.
func reloadRigOn(t *testing.T, r *rig) (*rig, *ReloadWatch, *events.Subscription) {
	t.Helper()
	r.register(HookConfig{ID: "old", Namespace: "jobs", Trigger: "k", ActionType: ActionTypePluginCall})
	r.build(nil)
	w, err := WatchReload(context.Background(), r.bus, reloadNS, reloadKind, "hooks-reload")
	if err != nil {
		t.Fatalf("WatchReload: %v", err)
	}
	obs, err := r.bus.Subscribe(context.Background(), reloadNS, "observer", 16)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	return r, w, obs
}

// runWatch starts w.Run over src with the dispatcher's own parser.
func runWatch(t *testing.T, r *rig, w *ReloadWatch, src func() (any, error)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	parse := func(raw any) ([]HookConfig, error) { return ParseHooksSection(raw, r.d.Runnable) }
	go func() { defer close(done); _ = w.Run(ctx, src, parse, r.d) }()
	t.Cleanup(func() { cancel(); <-done })
}

// awaitRejection reads the reload namespace until a rejection and returns
// its raw payload.
func awaitRejection(t *testing.T, obs *events.Subscription) string {
	t.Helper()
	for {
		select {
		case ev := <-obs.Events:
			if ev.Kind == EventKindReloadRejected {
				return string(ev.Payload)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no rejection published")
		}
	}
}

// TestInvalidHooksReloadKeepsPreviousSet proves a refused reload leaves
// the previous hook firing and rejects with the parser's message and no
// param value.
func TestInvalidHooksReloadKeepsPreviousSet(t *testing.T) {
	r, w, obs := reloadRig(t)
	sub := r.audit()
	r.start()
	runWatch(t, r, w, sequenceSource(invalidSection))
	r.publish(reloadNS, string(reloadKind))
	payload := awaitRejection(t, obs)
	var body map[string]string
	if err := json.Unmarshal([]byte(payload), &body); err != nil || len(body) != 1 ||
		!strings.Contains(body["error"], `entry 0 key "bogus"`) || strings.Contains(payload, leakValue) {
		t.Fatalf("rejection payload = %s", payload)
	}
	r.publish("jobs", "k")
	if fire, _ := nextFire(t, sub); fire.HookID != "old" || fire.ResultCode != ResultSuccess {
		t.Fatalf("fire = %+v, want the previous hook", fire)
	}
	if hooks := r.d.Hooks(); len(hooks) != 1 || hooks[0].ID != "old" {
		t.Fatalf("hooks = %+v", hooks)
	}
}

// TestValidHooksReloadSwaps proves a valid reload replaces the set: the new
// hook fires on its namespace and the old one no longer does.
func TestValidHooksReloadSwaps(t *testing.T) {
	r, w, obs := reloadRig(t)
	sub := r.audit()
	r.start()
	runWatch(t, r, w, sequenceSource(validSection, invalidSection))
	r.publish(reloadNS, string(reloadKind))
	r.publish(reloadNS, string(reloadKind)) // barrier: rejected after the swap
	awaitRejection(t, obs)
	if hooks := r.d.Hooks(); len(hooks) != 1 || hooks[0].ID != "deployed" {
		t.Fatalf("hooks after reload = %+v", hooks)
	}
	r.publish("jobs", "k")
	r.publish("deploy", "done")
	if fire, _ := nextFire(t, sub); fire.HookID != "deployed" {
		t.Fatalf("fire = %+v, want only the reloaded hook", fire)
	}
	noMoreFires(t, sub)
}

// TestReloadAcceptedBeforeRunNotLost proves a reload published after
// WatchReload and before Run is applied once Run starts.
func TestReloadAcceptedBeforeRunNotLost(t *testing.T) {
	r, w, obs := reloadRig(t)
	r.publish(reloadNS, string(reloadKind))
	r.publish(reloadNS, string(reloadKind))
	runWatch(t, r, w, sequenceSource(validSection, invalidSection))
	awaitRejection(t, obs)
	if hooks := r.d.Hooks(); len(hooks) != 1 || hooks[0].ID != "deployed" {
		t.Fatalf("the pre-Run reload was lost: hooks = %+v", hooks)
	}
}

// TestReloadRejectsUnreadableSourceAndBadArgs covers a failing source (its
// text is withheld), a Register refusal, and argument validation.
func TestReloadRejectsUnreadableSourceAndBadArgs(t *testing.T) {
	r, w, obs := reloadRig(t)
	src := func() (any, error) { return nil, errorString("cannot read line: token=" + leakValue) }
	runWatch(t, r, w, src)
	r.publish(reloadNS, string(reloadKind))
	if payload := awaitRejection(t, obs); strings.Contains(payload, leakValue) || !strings.Contains(payload, "unreadable") {
		t.Fatalf("rejection payload = %s", payload)
	}
	if _, err := WatchReload(context.Background(), nil, reloadNS, reloadKind, "c"); err == nil {
		t.Fatal("WatchReload accepted a nil bus")
	}
	w2, err := WatchReload(context.Background(), r.bus, "other", reloadKind, "c2")
	if err != nil {
		t.Fatalf("WatchReload: %v", err)
	}
	if err := w2.Run(context.Background(), nil, nil, nil); err == nil {
		t.Fatal("Run accepted nil arguments")
	}
}

// errorString is a plain error.
type errorString string

func (e errorString) Error() string { return string(e) }

// faultyStore is a MemStore whose Put or Get fails for one named key, so a
// test can fail one cursor write or read inside the bus. onPutFail, set
// before use, runs when an injected Put failure fires.
type faultyStore struct {
	*storetest.MemStore
	failPut, failGet atomic.Value // string: the key that fails
	onPutFail        func()
}

func (f *faultyStore) Put(ctx context.Context, ns, key string, v []byte) error {
	if k, _ := f.failPut.Load().(string); k != "" && k == key {
		if f.onPutFail != nil {
			f.onPutFail()
		}
		return errors.New("injected store failure")
	}
	return f.MemStore.Put(ctx, ns, key, v)
}

func (f *faultyStore) Get(ctx context.Context, ns, key string) ([]byte, error) {
	if k, _ := f.failGet.Load().(string); k != "" && k == key {
		return nil, errors.New("injected store failure")
	}
	return f.MemStore.Get(ctx, ns, key)
}

// faultyRig is a rig whose bus runs over a faultyStore.
func faultyRig(t *testing.T) (*rig, *faultyStore) {
	t.Helper()
	r := newRig(t)
	r.store = storetest.NewMemStore()
	fs := &faultyStore{MemStore: r.store}
	r.bus = events.New(fs, r.clock)
	t.Cleanup(func() { _ = r.bus.Close() })
	return r, fs
}

// TestSwapCursorResetFailureNeverReplays proves a Swap whose cursor reset
// fails for a re-added namespace keeps it out of the persisted set and the
// previous registry in force, so the restart that follows starts it at
// head instead of replaying the 50-event backlog. The failure models a
// crash: from then on every namespace-set write fails, so no unwind can
// repair a set written too early.
func TestSwapCursorResetFailureNeverReplays(t *testing.T) {
	r, fs := faultyRig(t)
	state := &faultyStore{MemStore: r.state}
	fs.onPutFail = func() { state.failPut.Store(namespacesKey) }
	withState := func(c *DispatcherConfig) { c.State = state }
	r.reg = registryOf(t, "a", "c")
	r.build(withState)
	stop := r.startStoppable()
	if err := r.d.Swap(registryOf(t, "a")); err != nil {
		t.Fatalf("Swap(remove c): %v", err)
	}
	r.publishN("c", "k", 50)
	fs.failPut.Store("cursor:hooks:c")
	if err := r.d.Swap(registryOf(t, "a", "c")); err == nil {
		t.Fatal("Swap(re-add c) succeeded over a failing cursor write")
	}
	if set, hooks := r.persistedSet(), r.d.Hooks(); set != "a" || len(hooks) != 1 || hooks[0].ID != "a" {
		t.Errorf("after the failed Swap: persisted %q, hooks %+v; want a and the previous registry", set, hooks)
	}
	stop()
	fs.failPut.Store("")
	state.failPut.Store("")
	r.reg = registryOf(t, "a", "c")
	r.build(withState)
	sub := r.audit()
	r.start()
	next := r.publish("c", "k")
	if fire, _ := nextFire(t, sub); fire.EventSeq != next.Seq {
		t.Fatalf("first fire after restart is seq %d, want only %d: the backlog replayed", fire.EventSeq, next.Seq)
	}
}

// TestFailedSwapKeepsPreviousRegistry proves a reload whose Swap fails at a
// subscribe, after another added namespace already subscribed, keeps the
// previous hook live, the persisted set unchanged and no consumer on the
// added namespaces, and publishes hooks.reload.rejected.
func TestFailedSwapKeepsPreviousRegistry(t *testing.T) {
	r, fs := faultyRig(t)
	r, w, obs := reloadRigOn(t, r)
	fs.failGet.Store("cursor:hooks:zeta")
	sub := r.audit()
	r.start()
	twoNamespaces := []any{
		map[string]any{"id": "deployed", "namespace": "deploy", "trigger": "done", "action_type": "agent-note"},
		map[string]any{"id": "zeta", "namespace": "zeta", "trigger": "done", "action_type": "agent-note"}}
	runWatch(t, r, w, sequenceSource(twoNamespaces))
	r.publish(reloadNS, string(reloadKind))
	awaitRejection(t, obs)
	if hooks, set := r.d.Hooks(), r.persistedSet(); len(hooks) != 1 || hooks[0].ID != "old" || set != "jobs" {
		t.Fatalf("after the failed Swap: hooks %+v, persisted %q; want the previous set", hooks, set)
	}
	held, err := r.bus.Subscribe(context.Background(), "deploy", "hooks:deploy", 1)
	if err != nil {
		t.Fatalf("the failed Swap left a consumer on deploy: %v", err)
	}
	_ = held.Unsubscribe()
	r.publish("deploy", "done")
	r.publish("jobs", "k")
	if fire, _ := nextFire(t, sub); fire.HookID != "old" || fire.ResultCode != ResultSuccess {
		t.Fatalf("fire = %+v, want only the previous hook", fire)
	}
	noMoreFires(t, sub)
}
