// Purpose: Swap's single commit point. Whichever namespace-set write
//
//	fails during a running Swap, the persisted set names only namespaces
//	the dispatcher follows, so a restart that re-adds a namespace never
//	replays what landed on it after the failure.
//
// Constraints: the persisted set and the live subscriptions are read
//
//	directly, so the invariant is asserted on stored state; fires are
//	counted only as the restart's observable consequence.
package hooks

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
)

// nthPutStore fails the nth Put (1-based, counted from arm) of key.
type nthPutStore struct {
	*faultyStore
	key     string
	n, seen atomic.Int32
}

func (s *nthPutStore) arm(n int32) { s.seen.Store(0); s.n.Store(n) }

func (s *nthPutStore) Put(ctx context.Context, ns, key string, v []byte) error {
	if key == s.key && s.seen.Add(1) == s.n.Load() {
		return errors.New("injected store failure")
	}
	return s.faultyStore.Put(ctx, ns, key, v)
}

// commitRig is a faultyRig whose State fails the nth namespace-set write.
func commitRig(t *testing.T, namespaces ...string) (*rig, *faultyStore, *nthPutStore) {
	t.Helper()
	r, fs := faultyRig(t)
	state := &nthPutStore{faultyStore: &faultyStore{MemStore: r.state}, key: namespacesKey}
	r.reg = registryOf(t, namespaces...)
	r.build(func(c *DispatcherConfig) { c.State = state })
	return r, fs, state
}

// followed lists the namespaces with a live subscription.
func (r *rig) followed() string {
	r.d.swapMu.Lock()
	defer r.d.swapMu.Unlock()
	var out []string
	for ns := range r.d.subs {
		out = append(out, ns)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// sortedSet orders a comma-joined set, so sets compare by membership.
func sortedSet(joined string) string {
	parts := strings.Split(joined, ",")
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// restartCounting rebuilds the dispatcher over the same stores with
// namespaces, publishes one event on each of probe, and counts, per
// namespace/seq, the fires on probe namespaces for any other event seen on
// the audit namespace (from its first event) before those arrive.
func restartCounting(t *testing.T, r *rig, state *nthPutStore, namespaces, probe []string) map[string]int {
	t.Helper()
	r.reg = registryOf(t, namespaces...)
	r.build(func(c *DispatcherConfig) { c.State = state })
	sub := r.audit()
	r.start()
	fresh := map[string]uint64{}
	for _, ns := range probe {
		fresh[ns] = r.publish(ns, "k").Seq
	}
	other := map[string]int{}
	for seen := 0; seen < len(probe); {
		fire, _ := nextFire(t, sub)
		want, ok := fresh[fire.Namespace]
		switch {
		case ok && fire.EventSeq == want:
			seen++
		case ok:
			other[fmt.Sprintf("%s/%d", fire.Namespace, fire.EventSeq)]++
		}
	}
	return other
}

// TestSwapCommitFailureNeverStrandsRemovedNamespace: Swap {a,b} -> {a,c}
// with the kth namespace-set write failing (and, in one mode, b's cursor
// reset too). Afterwards the persisted set equals the followed set; b then
// receives 30 events; a restart that re-adds b fires none of them unless b
// stayed configured, and then each at most once (fired live, never again).
func TestSwapCommitFailureNeverStrandsRemovedNamespace(t *testing.T) {
	for k := int32(1); k <= 3; k++ {
		for _, resetB := range []bool{false, true} {
			t.Run(fmt.Sprintf("write%d/resetFails=%v", k, resetB), func(t *testing.T) {
				r, fs, state := commitRig(t, "a", "b")
				stop := r.startStoppable()
				state.arm(k)
				if resetB {
					fs.failPut.Store("cursor:hooks:b")
				}
				err := r.d.Swap(registryOf(t, "a", "c"))
				state.arm(0)
				fs.failPut.Store("")
				if set, live := sortedSet(r.persistedSet()), r.followed(); set != live {
					t.Errorf("Swap err=%v: persisted %q, followed %q; the set names an unfollowed namespace", err, set, live)
				}
				r.publishN("b", "k", 30)
				stop()
				got := restartCounting(t, r, state, []string{"a", "b", "c"}, []string{"b"})
				for key, n := range got {
					if err == nil || n > 1 {
						t.Fatalf("Swap err=%v: event %s fired %d times after its namespace left; the backlog replayed", err, key, n)
					}
				}
			})
		}
	}
}

// TestSwapAddFailureLeavesSetAtLive: Swap {a} -> {a,c,d} where d's
// subscribe fails and the kth namespace-set write fails. The persisted
// set, the followed set and the registry stay a; events on c and d after
// the failure never fire after a restart with {a,c,d}.
func TestSwapAddFailureLeavesSetAtLive(t *testing.T) {
	for k := int32(1); k <= 4; k++ {
		t.Run(fmt.Sprintf("write%d", k), func(t *testing.T) {
			r, fs, state := commitRig(t, "a", "c", "d")
			stop := r.startStoppable()
			if err := r.d.Swap(registryOf(t, "a")); err != nil {
				t.Fatalf("Swap(remove c,d): %v", err)
			}
			state.arm(k)
			fs.failGet.Store("cursor:hooks:d")
			if err := r.d.Swap(registryOf(t, "a", "c", "d")); err == nil {
				t.Fatal("Swap succeeded over a failing subscribe")
			}
			state.arm(0)
			fs.failGet.Store("")
			if set, live, hooks := r.persistedSet(), r.followed(), r.d.Hooks(); set != "a" || live != "a" ||
				len(hooks) != 1 || hooks[0].ID != "a" {
				t.Errorf("after the failed Swap: persisted %q, followed %q, hooks %+v; want a", set, live, hooks)
			}
			r.publishN("c", "k", 10)
			r.publishN("d", "k", 10)
			stop()
			if got := restartCounting(t, r, state, []string{"a", "c", "d"}, []string{"c", "d"}); len(got) != 0 {
				t.Fatalf("fires of events published after the failed Swap %v; the backlog replayed", got)
			}
		})
	}
}
