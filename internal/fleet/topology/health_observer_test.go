package topology

import (
	"context"
	"strings"
	"testing"
)

type healthCall struct {
	lane     LaneID
	from, to LaneHealth
}

// seededLaneStore reconciles one synthetic lane into a real store and
// returns the store plus that lane's current row.
func seededLaneStore(t *testing.T) (*Store, Lane) {
	t.Helper()
	ctx := context.Background()
	reg := newTestRegistry(t)
	seedOAuthProviderWithLane(t, reg)
	store := newTestStore(t)
	if _, err := Reconcile(ctx, store, reg, newTestClock()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	lanes, err := store.ListLanes(ctx, false)
	if err != nil || len(lanes) != 1 {
		t.Fatalf("ListLanes = %d lanes, %v; want 1", len(lanes), err)
	}
	return store, lanes[0]
}

func recordObserver(t *testing.T, s *Store) *[]healthCall {
	t.Helper()
	calls := &[]healthCall{}
	if err := s.SetHealthObserver(func(l LaneID, from, to LaneHealth) {
		*calls = append(*calls, healthCall{l, from, to})
	}); err != nil {
		t.Fatalf("SetHealthObserver: %v", err)
	}
	return calls
}

func otherHealth(h LaneHealth) LaneHealth {
	if h == LaneHealthExhausted {
		return LaneHealthAvailable
	}
	return LaneHealthExhausted
}

func TestLaneHealthTransitionPublishes(t *testing.T) {
	ctx := context.Background()
	store, lane := seededLaneStore(t)
	calls := recordObserver(t, store)

	if err := store.UpsertLanes(ctx, []Lane{lane}); err != nil {
		t.Fatalf("unchanged UpsertLanes: %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("unchanged health published %+v, want none", *calls)
	}

	from, to := lane.Health, otherHealth(lane.Health)
	changed := lane
	changed.Health = to
	if err := store.UpsertLanes(ctx, []Lane{changed}); err != nil {
		t.Fatalf("changed UpsertLanes: %v", err)
	}
	want := healthCall{lane.ID, from, to}
	if len(*calls) != 1 || (*calls)[0] != want {
		t.Fatalf("calls = %+v, want exactly [%+v]", *calls, want)
	}
	got, err := store.GetLane(ctx, lane.ID)
	if err != nil || got.Health != to {
		t.Fatalf("GetLane health = %q, %v; want %q committed", got.Health, err, to)
	}
}

func TestLaneHealthRolledBackTxPublishesNothing(t *testing.T) {
	ctx := context.Background()
	store, lane := seededLaneStore(t)
	calls := recordObserver(t, store)

	changed := lane
	changed.Health = otherHealth(lane.Health)
	invalid := Lane{ID: "lane-synthetic-bad"} // fails UpsertLane validation
	if err := store.UpsertLanes(ctx, []Lane{changed, invalid}); err == nil {
		t.Fatal("UpsertLanes with an invalid lane = nil, want error")
	}
	if len(*calls) != 0 {
		t.Fatalf("rolled-back tx published %+v, want none", *calls)
	}
	got, err := store.GetLane(ctx, lane.ID)
	if err != nil || got.Health != lane.Health {
		t.Fatalf("GetLane health = %q, %v; want prior %q", got.Health, err, lane.Health)
	}

	err = store.SetHealthObserver(func(LaneID, LaneHealth, LaneHealth) {})
	if err != errHealthObserverSet || !strings.Contains(err.Error(), "topology: lane health observer already set") {
		t.Fatalf("second SetHealthObserver = %v, want errHealthObserverSet", err)
	}
	if err := store.SetHealthObserver(nil); err != errNilHealthObserver {
		t.Fatalf("nil SetHealthObserver = %v, want errNilHealthObserver", err)
	}
}
