// Purpose: the fire budget over real runner publishes: the depth bound on
//
//	a two-hook cycle, the per-root fan-out bound across many events of one
//	chain, lineage through a runner's context, the per-hook rate backstop
//	for a loop that drops that context, and the redelivery check.
package hooks

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
)

// publishOnRun makes runner publish kind on ns with the context it was run
// with (the dispatcher's chain) or, when dropCtx, with a bare one.
func publishOnRun(r *rig, ns, kind string, dropCtx bool) func(context.Context, Fire, map[string]string) error {
	return func(ctx context.Context, _ Fire, _ map[string]string) error {
		if dropCtx {
			ctx = context.Background()
		}
		_, err := r.bus.Publish(ctx, ns, events.EventKind(kind), "runner", nil)
		return err
	}
}

// fireUntilBudget reads fires until the first budget refusal, failing if
// more than limit non-budget fires arrive first.
func fireUntilBudget(t *testing.T, sub *events.Subscription, limit int) (ran []HookFire, refused HookFire) {
	t.Helper()
	for {
		fire, _ := nextFire(t, sub)
		if fire.ResultCode == ResultBudget {
			return ran, fire
		}
		if fire.ResultCode != ResultSuccess {
			t.Fatalf("unexpected fire %+v", fire)
		}
		if ran = append(ran, fire); len(ran) > limit {
			t.Fatalf("more than %d fires ran before any budget refusal", limit)
		}
	}
}

// TestHookCycleBoundedByDepth: hooks A and B re-trigger each other through
// runners that publish with the runner context. Depths 0..4 run, the
// depth-5 fire is refused as budget, and nothing runs or publishes after.
func TestHookCycleBoundedByDepth(t *testing.T) {
	r := newRig(t)
	r.register(HookConfig{ID: "A", Namespace: "loop", Trigger: "a", ActionType: ActionTypePluginCall})
	r.register(HookConfig{ID: "B", Namespace: "loop", Trigger: "b", ActionType: ActionTypeAgentNote})
	r.plugin.onRun = publishOnRun(r, "loop", "b", false)
	r.note.onRun = publishOnRun(r, "loop", "a", false)
	r.build(nil)
	sub := r.audit()
	r.start()
	root := r.publish("loop", "a")
	ran, refused := fireUntilBudget(t, sub, MaxChainDepth+1)
	if len(ran) != MaxChainDepth+1 {
		t.Fatalf("%d fires ran, want depths 0..%d", len(ran), MaxChainDepth)
	}
	for i, f := range ran {
		if f.Depth != i {
			t.Fatalf("fire %d depth = %d", i, f.Depth)
		}
	}
	if refused.HookID != "B" || refused.Depth != MaxChainDepth+1 {
		t.Fatalf("refused = %+v, want B at depth 5", refused)
	}
	noMoreFires(t, sub)
	if r.plugin.count() != 3 || r.note.count() != 2 || r.d.Stats().BudgetRefusals != 1 {
		t.Fatalf("runs A=%d B=%d refusals=%d", r.plugin.count(), r.note.count(), r.d.Stats().BudgetRefusals)
	}
	stored, err := r.bus.Replay(context.Background(), "loop", 0)
	if err != nil || len(stored) != 6 || stored[0].Seq != root.Seq {
		t.Fatalf("loop namespace holds %d events (%v), want the root plus 5 runner publishes", len(stored), err)
	}
}

// TestHookFanOutBoundedPerRoot: one root fires A, whose runner publishes
// 40 distinct kinds each matched by its own hook, so no single event and
// no single hook exceeds a limit; the chain as a whole admits 32 fires.
func TestHookFanOutBoundedPerRoot(t *testing.T) {
	const children = 40
	r := newRig(t)
	r.register(HookConfig{ID: "A", Namespace: "fan", Trigger: "root", ActionType: ActionTypePluginCall})
	for i := 0; i < children; i++ {
		r.register(HookConfig{ID: fmt.Sprintf("B%02d", i), Namespace: "fan", Trigger: fmt.Sprintf("k%02d", i),
			ActionType: ActionTypeAgentNote})
	}
	r.plugin.onRun = func(ctx context.Context, _ Fire, _ map[string]string) error {
		for i := 0; i < children; i++ {
			if _, err := r.bus.Publish(ctx, "fan", events.EventKind(fmt.Sprintf("k%02d", i)), "A", nil); err != nil {
				return err
			}
		}
		return nil
	}
	r.build(nil)
	sub := r.audit()
	r.start()
	r.publish("fan", "root")
	budget := 0
	for i := 0; i < children+1; i++ {
		if fire, _ := nextFire(t, sub); fire.ResultCode == ResultBudget {
			budget++
		}
	}
	if r.plugin.count()+r.note.count() != MaxFiresPerRoot || budget != children+1-MaxFiresPerRoot {
		t.Fatalf("ran %d, refused %d; want %d and %d", r.plugin.count()+r.note.count(), budget,
			MaxFiresPerRoot, children+1-MaxFiresPerRoot)
	}
	if got := r.d.Stats().BudgetRefusals; got != uint64(budget) {
		t.Fatalf("Stats.BudgetRefusals = %d, want %d", got, budget)
	}
}

// TestHookChainPropagatesThroughRunnerPublish proves a publish made with
// the runner's context fires the next hook at depth+1 on the same root,
// and the bus records that chain for the published event.
func TestHookChainPropagatesThroughRunnerPublish(t *testing.T) {
	r := newRig(t)
	r.register(HookConfig{ID: "A", Namespace: "chain", Trigger: "a", ActionType: ActionTypePluginCall})
	r.register(HookConfig{ID: "B", Namespace: "chain", Trigger: "b", ActionType: ActionTypeAgentNote})
	r.plugin.onRun = publishOnRun(r, "chain", "b", false)
	r.build(nil)
	sub := r.audit()
	r.start()
	root := r.publish("chain", "a")
	first, _ := nextFire(t, sub)
	second, _ := nextFire(t, sub)
	if first.HookID != "A" || first.Depth != 0 || second.HookID != "B" || second.Depth != 1 {
		t.Fatalf("fires = %+v / %+v", first, second)
	}
	fires, _ := r.note.snapshot()
	want := events.Cause{RootNamespace: "chain", RootSeq: root.Seq, Depth: 1}
	if len(fires) != 1 || fires[0].Chain != want {
		t.Fatalf("B's chain = %+v, want %+v", fires, want)
	}
	if c, ok := r.bus.CauseOf("chain", second.EventSeq); !ok || c != (events.Cause{RootNamespace: "chain", RootSeq: root.Seq}) {
		t.Fatalf("CauseOf(B's event) = %+v, %v; want A's chain", c, ok)
	}
}

// TestHookRateBackstopRefusesUntrackedLoop: a runner re-publishes its own
// trigger with a bare context, so every fire is a new root. On a frozen
// clock 32 fires run and the 33rd is budget; 59s later it is still
// refused; at +60s the loop runs 32 more.
func TestHookRateBackstopRefusesUntrackedLoop(t *testing.T) {
	r := newRig(t)
	r.register(HookConfig{ID: "L", Namespace: "rate", Trigger: "x", ActionType: ActionTypePluginCall})
	r.plugin.onRun = publishOnRun(r, "rate", "x", true)
	r.build(nil)
	sub := r.audit()
	r.start()
	r.publish("rate", "x")
	if ran, refused := fireUntilBudget(t, sub, MaxFiresPerHookPerWindow); len(ran) != MaxFiresPerHookPerWindow || refused.Depth != 0 {
		t.Fatalf("first window ran %d (refused %+v), want %d", len(ran), refused, MaxFiresPerHookPerWindow)
	}
	noMoreFires(t, sub)
	r.clock.Advance(RateWindow - time.Second)
	r.publish("rate", "x")
	if fire, _ := nextFire(t, sub); fire.ResultCode != ResultBudget {
		t.Fatalf("fire inside the window = %+v, want budget", fire)
	}
	r.clock.Advance(time.Second)
	r.publish("rate", "x")
	if ran, _ := fireUntilBudget(t, sub, MaxFiresPerHookPerWindow); len(ran) != MaxFiresPerHookPerWindow {
		t.Fatalf("second window ran %d, want %d", len(ran), MaxFiresPerHookPerWindow)
	}
	if r.plugin.count() != 2*MaxFiresPerHookPerWindow {
		t.Fatalf("runner count = %d", r.plugin.count())
	}
}

// TestRedeliveredFireRecordedAsDuplicate proves the same hook on the same
// event runs once; the redelivery is recorded as duplicate.
func TestRedeliveredFireRecordedAsDuplicate(t *testing.T) {
	r := newRig(t)
	r.build(nil)
	hook := HookConfig{ID: "d", Namespace: "jobs", ActionType: ActionTypeAgentNote}
	first, _ := r.d.dispatchHook(context.Background(), hook, "jobs", 7)
	second, _ := r.d.dispatchHook(context.Background(), hook, "jobs", 7)
	if first.ResultCode != ResultSuccess || second.ResultCode != ResultDuplicate || r.note.count() != 1 {
		t.Fatalf("first %q second %q runs %d", first.ResultCode, second.ResultCode, r.note.count())
	}
}
