// Purpose: per-namespace subscriptions. Every namespace a hook names fires,
//
//	others never do; Swap adds namespaces at their head and drops removed
//	ones with their cursor reset; a namespace re-added after Swap or
//	across a restart never replays its backlog; Run start resets every
//	cursor whose namespace changed membership.
//
// Constraints: cursors and the persisted namespace set are read from the
//
//	stores, so the assertions are on stored state, not only on fires.
package hooks

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// storedCursor reads the dispatcher's committed cursor for ns.
func (r *rig) storedCursor(ns string) uint64 {
	r.t.Helper()
	raw, err := r.store.Get(context.Background(), ns, "cursor:hooks:"+ns)
	if err != nil {
		r.t.Fatalf("cursor for %q: %v", ns, err)
	}
	return binary.BigEndian.Uint64(raw)
}

// persistedSet reads the dispatcher's persisted namespace set.
func (r *rig) persistedSet() string {
	r.t.Helper()
	raw, err := r.state.Get(context.Background(), dispatcherStateNamespace, namespacesKey)
	if err != nil {
		r.t.Fatalf("namespace set: %v", err)
	}
	var set []string
	if err := json.Unmarshal(raw, &set); err != nil {
		r.t.Fatalf("namespace set: %v", err)
	}
	return strings.Join(set, ",")
}

// registryOf builds a registry holding one plugin-call hook per namespace,
// each triggered by kind "k" and identified by its namespace.
func registryOf(t *testing.T, namespaces ...string) *Registry {
	t.Helper()
	reg := NewRegistry()
	for _, ns := range namespaces {
		if _, err := reg.Register(HookConfig{ID: ns, Namespace: ns, Trigger: "k", ActionType: ActionTypePluginCall}); err != nil {
			t.Fatalf("Register(%s): %v", ns, err)
		}
	}
	return reg
}

func (r *rig) publishN(ns, kind string, n int) {
	for i := 0; i < n; i++ {
		r.publish(ns, kind)
	}
}

// TestDispatcherSubscribesEveryHookNamespace: hooks on two namespaces both
// fire; the same kind on a third namespace fires nothing.
func TestDispatcherSubscribesEveryHookNamespace(t *testing.T) {
	r := newRig(t)
	r.reg = registryOf(t, "daemon", "jobs.gate")
	r.build(nil)
	sub := r.audit()
	r.start()
	r.publish("fleet.sessions", "k")
	r.publish("daemon", "k")
	r.publish("jobs.gate", "k")
	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		fire, _ := nextFire(t, sub)
		got[fire.Namespace+"/"+fire.HookID] = true
	}
	if !got["daemon/daemon"] || !got["jobs.gate/jobs.gate"] {
		t.Fatalf("fired = %v", got)
	}
	noMoreFires(t, sub)
	if r.persistedSet() != "daemon,jobs.gate" {
		t.Fatalf("persisted set = %q", r.persistedSet())
	}
}

// TestSwapAddsAndRemovesNamespaces: after Swap the removed namespace no
// longer fires and the added one starts at its head: its pre-Swap event
// never fires, the next one does.
func TestSwapAddsAndRemovesNamespaces(t *testing.T) {
	r := newRig(t)
	r.reg = registryOf(t, "a")
	r.build(nil)
	sub := r.audit()
	r.start()
	r.publish("b", "k")
	if err := r.d.Swap(registryOf(t, "b")); err != nil {
		t.Fatalf("Swap: %v", err)
	}
	r.publish("a", "k")
	next := r.publish("b", "k")
	fire, _ := nextFire(t, sub)
	if fire.Namespace != "b" || fire.EventSeq != next.Seq {
		t.Fatalf("fire = %+v, want only b's post-Swap event (seq %d)", fire, next.Seq)
	}
	noMoreFires(t, sub)
	if r.persistedSet() != "b" || len(r.d.Hooks()) != 1 || r.d.Hooks()[0].Namespace != "b" {
		t.Fatalf("persisted %q hooks %+v", r.persistedSet(), r.d.Hooks())
	}
}

// TestSwapReaddedNamespaceStartsAtHead: a namespace removed while its
// stored cursor lags head has that cursor reset to head in the store; 50
// events published while it is gone never fire after it is re-added.
func TestSwapReaddedNamespaceStartsAtHead(t *testing.T) {
	r := newRig(t)
	r.reg = registryOf(t, "a")
	r.build(nil)
	sub := r.audit()
	r.start()
	r.publishN("a", "other", 10)
	last := r.publish("a", "k")
	if fire, _ := nextFire(t, sub); fire.EventSeq != last.Seq {
		t.Fatalf("fire = %+v", fire)
	}
	// Stand in for a delivery that had not caught up when the removal
	// happened: the stored cursor lags head.
	lag := make([]byte, 8)
	binary.BigEndian.PutUint64(lag, 2)
	if err := r.store.Put(context.Background(), "a", "cursor:hooks:a", lag); err != nil {
		t.Fatalf("seed lag: %v", err)
	}
	if err := r.d.Swap(registryOf(t, "c")); err != nil {
		t.Fatalf("Swap(remove a): %v", err)
	}
	if got := r.storedCursor("a"); got != last.Seq {
		t.Fatalf("removed namespace cursor = %d, want head %d", got, last.Seq)
	}
	r.publishN("a", "k", 50)
	if err := r.d.Swap(registryOf(t, "a", "c")); err != nil {
		t.Fatalf("Swap(re-add a): %v", err)
	}
	next := r.publish("a", "k")
	if fire, _ := nextFire(t, sub); fire.EventSeq != next.Seq {
		t.Fatalf("fire = %+v, want only seq %d after the re-add", fire, next.Seq)
	}
	noMoreFires(t, sub)
}

// TestRestartReaddedNamespaceStartsAtHead: b is dropped while the
// dispatcher is stopped (a Swap while stopped, then a restart on a fresh
// Dispatcher over the same stores), 50 events land on b, b comes back:
// none of the backlog fires.
func TestRestartReaddedNamespaceStartsAtHead(t *testing.T) {
	r := newRig(t)
	r.reg = registryOf(t, "a", "b")
	r.build(nil)
	stop := r.startStoppable()
	stop()
	if err := r.d.Swap(registryOf(t, "a")); err != nil {
		t.Fatalf("Swap while stopped: %v", err)
	}
	r.publishN("b", "k", 50)
	if err := r.d.Swap(registryOf(t, "a", "b")); err != nil {
		t.Fatalf("Swap while stopped: %v", err)
	}
	r.publishN("b", "k", 20)
	// Restart as a new process would: fresh dispatcher, same stores, and a
	// configuration that dropped b while nothing ran.
	r.reg = registryOf(t, "a")
	r.build(nil)
	r.startStoppable()()
	r.publishN("b", "k", 30)
	r.reg = registryOf(t, "a", "b")
	r.build(nil)
	sub := r.audit()
	r.start()
	next := r.publish("b", "k")
	if fire, _ := nextFire(t, sub); fire.EventSeq != next.Seq {
		t.Fatalf("fire = %+v, want only seq %d; the backlog replayed", fire, next.Seq)
	}
	noMoreFires(t, sub)
}

// TestDispatcherRunResetsUnconfiguredCursors: a persisted namespace no
// longer configured (b) and a configured namespace missing from the
// persisted set (c) both have stale committed cursors; Run start moves
// both to head in the store and persists the configured set.
func TestDispatcherRunResetsUnconfiguredCursors(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	for _, ns := range []string{"b", "c"} {
		r.publishN(ns, "k", 3)
		stale, err := r.bus.Subscribe(ctx, ns, "hooks:"+ns, 8)
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		for i := 0; i < 3; i++ {
			<-stale.Events
		}
		_ = stale.Unsubscribe()
		r.publishN(ns, "k", 20)
	}
	if err := r.state.Put(ctx, dispatcherStateNamespace, namespacesKey, []byte(`["a","b"]`)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	r.reg = registryOf(t, "a", "c")
	r.build(nil)
	if r.storedCursor("b") != 3 || r.storedCursor("c") != 3 {
		t.Fatal("seeded cursors are not stale")
	}
	r.start()
	if b, c := r.storedCursor("b"), r.storedCursor("c"); b != 23 || c != 23 {
		t.Fatalf("cursors after Run start b=%d c=%d, want both at head 23", b, c)
	}
	if r.persistedSet() != "a,c" {
		t.Fatalf("persisted set = %q", r.persistedSet())
	}
}

// startStoppable runs the dispatcher and returns a func that stops it and
// waits for Run to return.
func (r *rig) startStoppable() func() {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errs, err := r.d.start(ctx)
	if err != nil {
		cancel()
		r.t.Fatalf("start: %v", err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = r.d.serve(ctx, errs) }()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			r.t.Fatal("Run did not stop")
		}
	}
}

// TestSwapRacesRunSafely swaps registries repeatedly while events flow, for
// the race detector, then proves the final set is the one in force.
func TestSwapRacesRunSafely(t *testing.T) {
	r := newRig(t)
	r.reg = registryOf(t, "a")
	r.build(nil)
	r.start()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_, _ = r.bus.Publish(context.Background(), "a", "k", "test", nil)
			_, _ = r.bus.Publish(context.Background(), "b", "k", "test", nil)
		}
	}()
	for i := 0; i < 20; i++ {
		regs := [][]string{{"a"}, {"b"}, {"a", "b"}}
		if err := r.d.Swap(registryOf(t, regs[i%3]...)); err != nil {
			t.Fatalf("Swap %d: %v", i, err)
		}
	}
	<-done
	if err := r.d.Swap(registryOf(t, "b")); err != nil {
		t.Fatalf("final Swap: %v", err)
	}
	if r.persistedSet() != "b" {
		t.Fatalf("persisted set = %q", r.persistedSet())
	}
}
