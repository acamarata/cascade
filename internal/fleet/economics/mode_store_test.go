package economics

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
)

func newTestStore(t *testing.T, clock *testkit.FrozenClock) *SchedulerModeStore {
	t.Helper()
	db := newTestDB(t)
	return NewSchedulerModeStore(db, clock)
}

func TestSchedulerModeStoreResolveLifecycleDefault(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	store := newTestStore(t, clock)
	ctx := context.Background()

	state, err := store.Resolve(ctx, "proj-1", "implement")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if state.Mode != ModeBuild || state.Source != ModeSourceLifecycle {
		t.Errorf("Resolve = %+v, want mode build, source lifecycle", state)
	}
}

func TestSchedulerModeStoreSetAndResolveExplicit(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	store := newTestStore(t, clock)
	ctx := context.Background()

	if _, err := store.Set(ctx, "proj-1", ModeVerify, 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	state, err := store.Resolve(ctx, "proj-1", "implement")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if state.Mode != ModeVerify || state.Source != ModeSourceExplicit {
		t.Errorf("Resolve = %+v, want explicit verify", state)
	}
}

func TestSchedulerModeStoreIncidentDefaultTTLAndRenewal(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	store := newTestStore(t, clock)
	ctx := context.Background()

	state, err := store.Set(ctx, "proj-1", ModeIncident, 0)
	if err != nil {
		t.Fatalf("Set incident: %v", err)
	}
	wantExpiry := clock.Now().Add(4 * time.Hour)
	if !state.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("ExpiresAt = %v, want default 4h TTL %v", state.ExpiresAt, wantExpiry)
	}

	clock.Advance(1 * time.Hour)
	renewed, err := store.Set(ctx, "proj-1", ModeIncident, 0)
	if err != nil {
		t.Fatalf("Set incident (renew): %v", err)
	}
	if !renewed.ExpiresAt.Equal(clock.Now().Add(4 * time.Hour)) {
		t.Errorf("renewed ExpiresAt = %v, want extended from the renewal instant", renewed.ExpiresAt)
	}
}

func TestSchedulerModeStoreExpiryAutoRevert(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	store := newTestStore(t, clock)
	ctx := context.Background()

	if _, err := store.Set(ctx, "proj-1", ModeIncident, 0); err != nil {
		t.Fatalf("Set incident: %v", err)
	}
	clock.Advance(4*time.Hour + time.Second)
	state, err := store.Resolve(ctx, "proj-1", "implement")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if state.Mode != ModeBuild || state.Source != ModeSourceLifecycle {
		t.Errorf("Resolve after expiry = %+v, want lifecycle-derived build", state)
	}
}

func TestSchedulerModeStoreTTLNotApplicable(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	store := newTestStore(t, clock)
	ctx := context.Background()

	if _, err := store.Set(ctx, "proj-1", ModeVerify, time.Hour); !errors.Is(err, ErrModeTTLNotApplicable) {
		t.Errorf("error = %v, want ErrModeTTLNotApplicable", err)
	}
}

func TestSchedulerModeStoreIncidentRenewalCap(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	store := newTestStore(t, clock)
	ctx := context.Background()

	// Three consecutive Sets succeed (R-21.118: at most three).
	for i := 0; i < 3; i++ {
		if _, err := store.Set(ctx, "proj-1", ModeIncident, 0); err != nil {
			t.Fatalf("Set #%d: %v", i+1, err)
		}
		clock.Advance(time.Minute)
	}
	// The fourth consecutive Set inside the same week is refused.
	if _, err := store.Set(ctx, "proj-1", ModeIncident, 0); !errors.Is(err, ErrIncidentRenewalCap) {
		t.Fatalf("4th Set error = %v, want ErrIncidentRenewalCap", err)
	}
	entries, err := store.IncidentJournal(ctx, "proj-1")
	if err != nil {
		t.Fatalf("IncidentJournal: %v", err)
	}
	if len(entries) != 1 || entries[0].Event != EventHumanApprovalRequired || entries[0].ApprovalID != ApprovalIDIncidentRenewalCap {
		t.Fatalf("journal = %+v, want one HUMAN_APPROVAL_REQUIRED entry naming %q", entries, ApprovalIDIncidentRenewalCap)
	}
	// The refused Set must not have changed the persisted mode: Resolve
	// still reports the live (unexpired) third incident Set.
	state, err := store.Resolve(ctx, "proj-1", "implement")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if state.Mode != ModeIncident {
		t.Errorf("Resolve after refused renewal = %+v, want mode still incident (no row change)", state)
	}
}

func TestSchedulerModeStoreIncidentRenewalKeyedByProject(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	store := newTestStore(t, clock)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := store.Set(ctx, "proj-1", ModeIncident, 0); err != nil {
			t.Fatalf("proj-1 Set #%d: %v", i+1, err)
		}
	}
	if _, err := store.Set(ctx, "proj-1", ModeIncident, 0); !errors.Is(err, ErrIncidentRenewalCap) {
		t.Fatalf("proj-1 4th Set error = %v, want ErrIncidentRenewalCap", err)
	}
	// A different project has its own, unconsumed renewal budget.
	if _, err := store.Set(ctx, "proj-2", ModeIncident, 0); err != nil {
		t.Fatalf("proj-2 Set: %v", err)
	}
}

func TestSchedulerModeStoreNonIncidentSetResetsRenewalCounters(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	store := newTestStore(t, clock)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := store.Set(ctx, "proj-1", ModeIncident, 0); err != nil {
			t.Fatalf("Set #%d: %v", i+1, err)
		}
	}
	if _, err := store.Set(ctx, "proj-1", ModeVerify, 0); err != nil {
		t.Fatalf("Set verify: %v", err)
	}
	// The counters reset, so three more consecutive incident Sets succeed.
	for i := 0; i < 3; i++ {
		if _, err := store.Set(ctx, "proj-1", ModeIncident, 0); err != nil {
			t.Fatalf("post-reset Set #%d: %v", i+1, err)
		}
	}
}

func TestSchedulerModeStoreClearResetsRenewalCounters(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	store := newTestStore(t, clock)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := store.Set(ctx, "proj-1", ModeIncident, 0); err != nil {
			t.Fatalf("Set #%d: %v", i+1, err)
		}
	}
	if err := store.Clear(ctx, "proj-1"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	state, err := store.Resolve(ctx, "proj-1", "implement")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if state.Source != ModeSourceLifecycle {
		t.Errorf("Resolve after Clear = %+v, want lifecycle-derived", state)
	}
	for i := 0; i < 3; i++ {
		if _, err := store.Set(ctx, "proj-1", ModeIncident, 0); err != nil {
			t.Fatalf("post-clear Set #%d: %v", i+1, err)
		}
	}
}

func TestSchedulerModeStoreWeekRolloverResetsRenewalCounters(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	store := newTestStore(t, clock)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := store.Set(ctx, "proj-1", ModeIncident, 0); err != nil {
			t.Fatalf("Set #%d: %v", i+1, err)
		}
	}
	clock.Advance(7*24*time.Hour + time.Second)
	if _, err := store.Set(ctx, "proj-1", ModeIncident, 0); err != nil {
		t.Fatalf("Set after week rollover: %v", err)
	}
}
