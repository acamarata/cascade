// Purpose: the fires ring and Stats the hooks RPCs read.
package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestDispatcherFiresRingKeepsLast100 records 101 fires: the ring keeps the
// newest 100, oldest first, and carries no params.
func TestDispatcherFiresRingKeepsLast100(t *testing.T) {
	const param = "ring-" + "param-value"
	r := newRig(t)
	r.build(nil)
	if len(r.d.Fires()) != 0 {
		t.Fatal("a new dispatcher has fires")
	}
	for i := 0; i <= firesRingSize; i++ {
		hook := HookConfig{ID: fmt.Sprintf("h%03d", i), Namespace: "jobs", ActionType: ActionTypeAgentNote,
			ActionParams: map[string]string{"note": param}}
		_, _ = r.d.dispatchHook(context.Background(), hook, "jobs", uint64(i+1))
	}
	ring := r.d.Fires()
	if len(ring) != firesRingSize || ring[0].HookID != "h001" || ring[len(ring)-1].HookID != "h100" {
		t.Fatalf("ring holds %d, first %q last %q; want 100, h001..h100", len(ring), ring[0].HookID, ring[len(ring)-1].HookID)
	}
	for i := 1; i < len(ring); i++ {
		if ring[i].EventSeq <= ring[i-1].EventSeq {
			t.Fatalf("ring is not oldest first at %d", i)
		}
	}
	raw, _ := json.Marshal(ring)
	if strings.Contains(string(raw), param) || strings.Contains(string(raw), "action_params") {
		t.Fatal("the ring carries params")
	}
	ring[0].HookID = "mutated"
	if r.d.Fires()[0].HookID == "mutated" {
		t.Fatal("Fires returned the ring itself, not a copy")
	}
}

// TestDispatcherStatsCountsBudgetRefusals fires one hook 40 times inside
// one rate window: 32 run, 8 are budget refusals, and Stats reports them
// with the registry size and the last fire time.
func TestDispatcherStatsCountsBudgetRefusals(t *testing.T) {
	r := newRig(t)
	hook := r.register(HookConfig{ID: "busy", Namespace: "jobs", Trigger: "k", ActionType: ActionTypeAgentNote})
	r.register(HookConfig{ID: "idle", Namespace: "jobs", Trigger: "z", ActionType: ActionTypeAgentNote})
	r.build(nil)
	if s := r.d.Stats(); s.Registered != 2 || !s.LastFire.IsZero() || s.BudgetRefusals != 0 {
		t.Fatalf("initial stats = %+v", s)
	}
	for i := 1; i <= 40; i++ {
		_, _ = r.d.dispatchHook(context.Background(), hook, "jobs", uint64(i))
	}
	r.clock.Advance(time.Minute)
	_, _ = r.d.dispatchHook(context.Background(), hook, "jobs", 41)
	s := r.d.Stats()
	if s.BudgetRefusals != 8 || r.note.count() != 33 || !s.LastFire.Equal(r.clock.Now()) || s.Registered != 2 {
		t.Fatalf("stats = %+v runs = %d", s, r.note.count())
	}
}
