// Purpose: the running-profile holder's contract — the atomic swap on a
// valid reload, the untouched running profile plus config.reload.rejected
// on an invalid one, the immediacy of a profile change (no cache, matching
// the grant model's no-cache decision), and the deny-everything behaviour
// of a controller that has loaded nothing.
//
// SPORT: internal/policy Controller/ADDED (P1-E09-W2-S18-T1).
package policy

import (
	"context"
	"sync"
	"testing"
)

// recorder collects the audit events a controller emits.
type recorder struct {
	mu     sync.Mutex
	events []string
	fields []map[string]interface{}
}

// emit is the AuditEmitter under test.
func (r *recorder) emit(_ context.Context, event string, fields map[string]interface{}) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	r.fields = append(r.fields, fields)
	return nil
}

// last returns the most recent event name, or "" when none was emitted.
func (r *recorder) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) == 0 {
		return ""
	}
	return r.events[len(r.events)-1]
}

// TestControllerWithNoProfileDeniesEverything is hard requirement 2's
// unset case at the API an engine actually calls: before any config has
// been applied, every level denies.
func TestControllerWithNoProfileDeniesEverything(t *testing.T) {
	c := NewController(nil)
	for lvl := L0; lvl <= L4; lvl++ {
		if got := c.VerdictFor(lvl); got != VerdictDeny {
			t.Errorf("an unloaded controller answered %s at %s", got, lvl)
		}
	}
	if c.Profile() != nil {
		t.Error("an unloaded controller reports a profile")
	}
	if got := c.Batching(); got != defaultApprovalBatching() {
		t.Errorf("an unloaded controller's batching = %+v, want the 08 §3 defaults", got)
	}
	if c.Profile().Name() != lockedProfileName {
		t.Errorf("an unloaded controller names its profile %q", c.Profile().Name())
	}
	// The nil controller is the same story one step further out.
	var nilController *Controller
	if got := nilController.VerdictFor(L0); got != VerdictDeny {
		t.Errorf("the nil controller answered %s at L0", got)
	}
	if got := nilController.Batching(); got != defaultApprovalBatching() {
		t.Errorf("the nil controller's batching = %+v, want the defaults", got)
	}
	if err := nilController.Apply(context.Background(), nil); err == nil {
		t.Error("Apply on a nil controller reported success")
	}
}

// TestProfileHotReloadAtomicSwap covers the valid-reload half of the
// hot-reload contract: the new profile is in force immediately and the load
// event names it.
func TestProfileHotReloadAtomicSwap(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	c := NewController(rec.emit)

	if err := c.Apply(ctx, policyTree(map[string]interface{}{"autonomy_profile": "balanced"})); err != nil {
		t.Fatalf("Apply(balanced): %v", err)
	}
	if got := c.VerdictFor(L1); got != VerdictAllow {
		t.Fatalf("balanced answered %s at L1, want allow", got)
	}
	if rec.last() != EventAutonomyProfileLoaded {
		t.Errorf("load emitted %q, want %q", rec.last(), EventAutonomyProfileLoaded)
	}

	// The swap: a stricter profile is in force on the very next call.
	// Nothing is memoised, so there is no stale verdict to expire.
	if err := c.Apply(ctx, policyTree(map[string]interface{}{
		"autonomy_profile":        "strict",
		"approval_batch_window_s": int64(45),
	})); err != nil {
		t.Fatalf("Apply(strict): %v", err)
	}
	if got := c.VerdictFor(L1); got != VerdictAsk {
		t.Errorf("after the swap L1 answered %s, want ask", got)
	}
	if got := c.Batching().WindowSeconds; got != 45 {
		t.Errorf("batching window = %d after the swap, want 45", got)
	}
	if got := c.Profile().Name(); got != "strict" {
		t.Errorf("running profile is %q after the swap", got)
	}
}

// TestProfileHotReloadRejectionKeepsRunningProfile covers the invalid half:
// the running profile survives untouched, config.reload.rejected is
// emitted, and the error reaches the caller.
func TestProfileHotReloadRejectionKeepsRunningProfile(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	c := NewController(rec.emit)
	if err := c.Apply(ctx, policyTree(map[string]interface{}{
		"autonomy_profile":   "strict",
		"approval_batch_cap": int64(7),
	})); err != nil {
		t.Fatalf("Apply(strict): %v", err)
	}

	bad := []map[string]interface{}{
		policyTree(map[string]interface{}{"autonomy_profile": "balancd"}),
		policyTree(map[string]interface{}{"autonomy_profil": "balanced"}),
		policyTree(map[string]interface{}{"autonomy_profile": "balanced", "allow": levelList("L3")}),
		policyTree(map[string]interface{}{"approval_batch_cap": int64(0)}),
		{"policy": "balanced"},
	}
	for i, tree := range bad {
		if err := c.Apply(ctx, tree); err == nil {
			t.Errorf("case %d: Apply accepted an invalid config", i)
		}
		if rec.last() != EventConfigReloadRejected {
			t.Errorf("case %d: emitted %q, want %q", i, rec.last(), EventConfigReloadRejected)
		}
		if got := c.Profile().Name(); got != "strict" {
			t.Errorf("case %d: running profile changed to %q", i, got)
		}
		if got := c.VerdictFor(L1); got != VerdictAsk {
			t.Errorf("case %d: L1 answered %s, want the running profile's ask", i, got)
		}
		if got := c.Batching().Cap; got != 7 {
			t.Errorf("case %d: batching cap changed to %d", i, got)
		}
	}
}

// TestRejectionEventNamesTheRunningProfile asserts the rejected event
// carries what an operator needs: which section refused, why, and what is
// still running.
func TestRejectionEventNamesTheRunningProfile(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	c := NewController(rec.emit)
	if err := c.Apply(ctx, policyTree(map[string]interface{}{"autonomy_profile": "balanced"})); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := c.Apply(ctx, policyTree(map[string]interface{}{"autonomy_profile": "nope"})); err == nil {
		t.Fatal("Apply accepted an unknown profile")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	fields := rec.fields[len(rec.fields)-1]
	if fields["section"] != policySectionKey {
		t.Errorf("rejected event names section %v", fields["section"])
	}
	if fields["running"] != "balanced" {
		t.Errorf("rejected event says the running profile is %v", fields["running"])
	}
	if reason, ok := fields["reason"].(string); !ok || reason == "" {
		t.Errorf("rejected event carries no reason: %v", fields["reason"])
	}
}

// TestControllerSurvivesAFailingEmitter is the allowed-fail leg: the audit
// domain is not wired yet, and neither a nil emitter nor a failing one may
// stop a valid profile from loading.
func TestControllerSurvivesAFailingEmitter(t *testing.T) {
	ctx := context.Background()
	failing := func(context.Context, string, map[string]interface{}) error {
		return context.Canceled
	}
	for name, c := range map[string]*Controller{
		"nil emitter":     NewController(nil),
		"failing emitter": NewController(failing),
	} {
		if err := c.Apply(ctx, policyTree(map[string]interface{}{"autonomy_profile": "strict"})); err != nil {
			t.Errorf("%s: Apply: %v", name, err)
		}
		if got := c.Profile().Name(); got != "strict" {
			t.Errorf("%s: profile is %q, want strict", name, got)
		}
	}
}

// TestConcurrentReloadAndRead is the -race assertion: readers see either
// the old profile or the new one, never a torn table, while reloads run.
func TestConcurrentReloadAndRead(t *testing.T) {
	ctx := context.Background()
	c := NewController(nil)
	if err := c.Apply(ctx, policyTree(map[string]interface{}{"autonomy_profile": "balanced"})); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			name := "balanced"
			if n%2 == 1 {
				name = "strict"
			}
			for j := 0; j < 50; j++ {
				_ = c.Apply(ctx, policyTree(map[string]interface{}{"autonomy_profile": name}))
			}
		}(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				v := c.VerdictFor(L1)
				if v != VerdictAllow && v != VerdictAsk {
					t.Errorf("a concurrent read saw %s at L1", v)
					return
				}
				if c.VerdictFor(L4) != VerdictDeny {
					t.Error("a concurrent read saw L4 permitted")
					return
				}
			}
		}()
	}
	wg.Wait()
}
