package docket

import (
	"reflect"
	"sort"
	"testing"
)

func TestAssumeDoneSchedulesDependent(t *testing.T) {
	ts := []SchedTicket{
		{ID: "T1", State: SchedParked},
		{ID: "T2", State: SchedPending, Deps: []string{"T1"}},
	}
	res := Schedule(ts, true)
	sort.Strings(res.Schedulable)
	if !reflect.DeepEqual(res.Schedulable, []string{"T2"}) {
		t.Fatalf("expected T2 schedulable, got %v", res.Schedulable)
	}
}

func TestWithoutFlagStaysBlocked(t *testing.T) {
	ts := []SchedTicket{
		{ID: "T1", State: SchedParked},
		{ID: "T2", State: SchedPending, Deps: []string{"T1"}},
	}
	res := Schedule(ts, false)
	sort.Strings(res.Blocked)
	if !reflect.DeepEqual(res.Blocked, []string{"T2"}) {
		t.Fatalf("expected T2 blocked, got %v", res.Blocked)
	}
}

func TestNonParkedParentUnaffected(t *testing.T) {
	ts := []SchedTicket{
		{ID: "T1", State: SchedRunning},
		{ID: "T2", State: SchedPending, Deps: []string{"T1"}},
	}
	res := Schedule(ts, true)
	sort.Strings(res.Blocked)
	if !reflect.DeepEqual(res.Blocked, []string{"T2"}) {
		t.Fatalf("expected T2 blocked, got %v", res.Blocked)
	}
}

func TestAssumeDoneIgnoresUnfinishedNonParked(t *testing.T) {
	ts := []SchedTicket{
		{ID: "T1", State: SchedParked},
		{ID: "T3", State: SchedPending},
		{ID: "T2", State: SchedPending, Deps: []string{"T1", "T3"}},
	}
	res := Schedule(ts, true)
	sort.Strings(res.Blocked)
	if !reflect.DeepEqual(res.Blocked, []string{"T2"}) {
		t.Fatalf("expected T2 blocked, got %v", res.Blocked)
	}
}
