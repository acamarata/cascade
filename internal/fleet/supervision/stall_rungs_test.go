package supervision

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

type rungFixture struct {
	sess    *fakeSessions
	dirs    *DirectiveStore
	retry   *sessionRetryer
	enrich  *sessionEnricher
	stalled int64
}

func newRungFixture(t *testing.T) *rungFixture {
	t.Helper()
	fx := &rungFixture{sess: newFakeSessions(), dirs: NewDirectiveStore(storetest.NewMemStore(), runtime.NewFixedClock(stallT0)), stalled: stallT0.UnixMilli()}
	cfg := RungConfig{Sessions: fx.sess, Directives: fx.dirs, HydrationEnabled: true, BudgetTokens: testBudget}
	lookup := func(id string) (StallEvent, bool) {
		return StallEvent{SessionID: id, StallKind: StallKindIdle, StalledSince: fx.stalled}, true
	}
	fx.retry = &sessionRetryer{cfg: cfg, lookup: lookup}
	fx.enrich = &sessionEnricher{cfg: cfg, lookup: lookup}
	return fx
}

// wantCascade asserts err is a *cascade.Error of kind with exactly msg;
// errors.Is compares Kind only, so identity and message are checked here.
func wantCascade(t *testing.T, err error, kind cascade.Kind, msg string) {
	t.Helper()
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != kind || ce.Msg != msg {
		t.Fatalf("err = %v, want kind %v with message %q", err, kind, msg)
	}
}

func TestSessionRetryerEnqueuesFixedDirective(t *testing.T) {
	ctx := context.Background()
	fx := newRungFixture(t)
	fx.sess.put("s1", "active", 1)
	if err := fx.retry.Retry(ctx, "s1"); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	got, err := fx.dirs.Drain(ctx, "s1")
	if err != nil || len(got) != 1 {
		t.Fatalf("Drain = (%v, %v), want exactly one directive", got, err)
	}
	want := "Cascade supervision: this session has made no progress since 2026-10-04T12:00:00Z (idle). Re-read the task, re-run the last failing step, and report its result."
	if got[0].Text != want || got[0].Kind != DirectiveRetry || got[0].StalledSince != fx.stalled || got[0].SessionID != "s1" {
		t.Errorf("directive = %+v, want the fixed retry text %q", got[0], want)
	}
	// An unknown stall record names "unknown", never session content.
	fx.retry.lookup = func(string) (StallEvent, bool) { return StallEvent{}, false }
	if err := fx.retry.Retry(ctx, "s1"); err != nil {
		t.Fatalf("Retry (no record): %v", err)
	}
	again, _ := fx.dirs.Drain(ctx, "s1")
	wantUnknown := "Cascade supervision: this session has made no progress since unknown (unknown). Re-read the task, re-run the last failing step, and report its result."
	if len(again) != 1 || again[0].Text != wantUnknown {
		t.Errorf("no-record directive = %+v, want %q", again, wantUnknown)
	}
}

func TestSessionRetryerUnknownSessionNotFound(t *testing.T) {
	fx := newRungFixture(t)
	err := fx.retry.Retry(context.Background(), "ghost")
	wantCascade(t, err, cascade.KindNotFound, "supervision: session not found")
	var ce *cascade.Error
	if errors.As(err, &ce) && ce.Err != sessions.ErrNotFound {
		t.Errorf("cause = %v, want the session store's ErrNotFound by identity", ce.Err)
	}
	if left, _ := fx.dirs.Drain(context.Background(), "ghost"); len(left) != 0 {
		t.Errorf("a refused retry queued %d directives, want 0", len(left))
	}
}

func TestSessionRetryerClosedSessionNotFound(t *testing.T) {
	fx := newRungFixture(t)
	fx.sess.put("s1", "closed", 1)
	wantCascade(t, fx.retry.Retry(context.Background(), "s1"), cascade.KindNotFound, "supervision: session is closed")
	if left, _ := fx.dirs.Drain(context.Background(), "s1"); len(left) != 0 {
		t.Errorf("a refused retry queued %d directives, want 0", len(left))
	}
}

func TestSessionRetryerHydrationDisabledUnavailable(t *testing.T) {
	fx := newRungFixture(t)
	fx.retry.cfg.HydrationEnabled = false
	fx.sess.put("s1", "active", 1)
	wantCascade(t, fx.retry.Retry(context.Background(), "s1"), cascade.KindUnavailable, "no delivery channel: [context.hydration].enabled is false")
	if left, _ := fx.dirs.Drain(context.Background(), "s1"); len(left) != 0 {
		t.Errorf("a refused retry queued %d directives, want 0", len(left))
	}
}

func TestSessionContextEnricherReportsBudget(t *testing.T) {
	ctx := context.Background()
	fx := newRungFixture(t)
	fx.sess.put("s1", "blocked", 1)
	added, err := fx.enrich.Enrich(ctx, "s1")
	if err != nil || added != testBudget {
		t.Fatalf("Enrich = (%d, %v), want (%d, nil)", added, err, testBudget)
	}
	got, _ := fx.dirs.Drain(ctx, "s1")
	if len(got) != 1 || got[0].Kind != DirectiveContext || got[0].Text != "Cascade supervision: extra context budget granted for this turn." {
		t.Errorf("directive = %+v, want the fixed context text", got)
	}
	fx.sess.put("s2", "closed", 1)
	added, err = fx.enrich.Enrich(ctx, "s2")
	wantCascade(t, err, cascade.KindNotFound, "supervision: session is closed")
	if added != 0 {
		t.Errorf("a refused Enrich reported added = %d, want 0", added)
	}
}

// outcome flattens a rung result so differing outcomes compare unequal.
func outcome(err error) string {
	if err == nil {
		return "ok"
	}
	var ce *cascade.Error
	if errors.As(err, &ce) {
		return fmt.Sprintf("%v|%s", ce.Kind, ce.Msg)
	}
	return "untyped|" + err.Error()
}

// TestRetryRungOutcomeDependsOnState drives one rung through four
// situations; a rung that answers the same thing every time fails.
func TestRetryRungOutcomeDependsOnState(t *testing.T) {
	ctx := context.Background()
	fx := newRungFixture(t)
	fx.sess.put("live", "active", 1)
	fx.sess.put("closed", "closed", 1)
	rung := fx.retry
	got := map[string]string{
		"live":    outcome(rung.Retry(ctx, "live")),
		"closed":  outcome(rung.Retry(ctx, "closed")),
		"unknown": outcome(rung.Retry(ctx, "ghost")),
	}
	rung.cfg.HydrationEnabled = false
	got["hydration-off"] = outcome(rung.Retry(ctx, "live"))
	seen := map[string]string{}
	for name, o := range got {
		if prior, dup := seen[o]; dup {
			t.Errorf("%s and %s share outcome %q: the rung ignores state", name, prior, o)
		}
		seen[o] = name
	}
	if got["live"] != "ok" || len(seen) != 4 {
		t.Errorf("outcomes = %v, want four distinct with live ok", got)
	}
}

// TestRungsFailClosedWithoutCollaborators: a nil lookup or store refuses
// typed; it never "succeeds" silently.
func TestRungsFailClosedWithoutCollaborators(t *testing.T) {
	ctx := context.Background()
	noLookup := &sessionRetryer{cfg: RungConfig{Directives: newTestDirectives(t), HydrationEnabled: true}}
	wantCascade(t, noLookup.Retry(ctx, "s1"), cascade.KindUnavailable, "supervision: rung has no session lookup or directive store")
	noStore := &sessionEnricher{cfg: RungConfig{Sessions: newFakeSessions(), HydrationEnabled: true, BudgetTokens: 1}}
	_, err := noStore.Enrich(ctx, "s1")
	wantCascade(t, err, cascade.KindUnavailable, "supervision: rung has no session lookup or directive store")
	broken := newFakeSessions()
	broken.getErr = cascade.New(cascade.KindUnavailable, "db down")
	cfg := RungConfig{Sessions: broken, Directives: newTestDirectives(t), HydrationEnabled: true}
	wantCascade(t, (&sessionRetryer{cfg: cfg}).Retry(ctx, "s1"), cascade.KindUnavailable, "supervision: session lookup failed")
	odd := newFakeSessions()
	odd.put("s1", "weird", 1)
	cfg.Sessions = odd
	wantCascade(t, (&sessionRetryer{cfg: cfg}).Retry(ctx, "s1"), cascade.KindInvalidInput, "supervision: unrecognised session state")
}

func TestRetryTextFormatsStalledSince(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got, want := retryText(at.UnixMilli(), StallKindBlocked), "Cascade supervision: this session has made no progress since 2026-01-02T03:04:05Z (blocked). Re-read the task, re-run the last failing step, and report its result."; got != want {
		t.Errorf("retryText = %q, want %q", got, want)
	}
}
