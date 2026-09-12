package fleet

import (
	"reflect"
	"testing"
	"time"
)

func TestBuildSummary_Empty(t *testing.T) {
	got := BuildSummary(nil)
	want := Summary{}
	if got != want {
		t.Fatalf("BuildSummary(nil) = %+v, want %+v", got, want)
	}
}

func TestBuildSummary_Partial(t *testing.T) {
	rows := []TopSessionRow{
		{SessionID: "s1", State: "running", Lane: "lane-a"},
		{SessionID: "s2", State: "stalled", Lane: ""},
	}
	got := BuildSummary(rows)
	want := Summary{SessionCount: 2, LanesBusy: 1, StalledCount: 1}
	if got != want {
		t.Fatalf("BuildSummary(partial) = %+v, want %+v", got, want)
	}
}

func TestBuildSummary_Full(t *testing.T) {
	rows := []TopSessionRow{
		{SessionID: "s1", State: "running", Lane: "lane-a"},
		{SessionID: "s2", State: "running", Lane: "lane-b"},
		{SessionID: "s3", State: "stalled", Lane: "lane-a"},
		{SessionID: "s4", State: "stalled", Lane: "lane-c"},
	}
	got := BuildSummary(rows)
	want := Summary{SessionCount: 4, LanesBusy: 3, StalledCount: 2}
	if got != want {
		t.Fatalf("BuildSummary(full) = %+v, want %+v", got, want)
	}
}

func TestNewTopSnapshot_DerivesSummary(t *testing.T) {
	rows := []TopSessionRow{{SessionID: "s1", State: "stalled"}}
	gov := GovernorPanel{Available: true, CPUFraction: 0.5}
	task := TaskPanel{Available: false, Unavailable: "no ticket context"}
	at := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)

	snap := NewTopSnapshot(rows, gov, task, at)

	if snap.Summary != (Summary{SessionCount: 1, StalledCount: 1}) {
		t.Fatalf("Summary = %+v, want session=1 stalled=1", snap.Summary)
	}
	if snap.Governor != gov {
		t.Fatalf("Governor = %+v, want %+v", snap.Governor, gov)
	}
	if !reflect.DeepEqual(snap.Task, task) {
		t.Fatalf("Task = %+v, want %+v", snap.Task, task)
	}
	if !snap.GeneratedAt.Equal(at) {
		t.Fatalf("GeneratedAt = %v, want %v", snap.GeneratedAt, at)
	}
}

func TestUpsertSession_InsertsNew(t *testing.T) {
	rows := []TopSessionRow{{SessionID: "s1", State: "running"}}
	got := UpsertSession(rows, TopSessionRow{SessionID: "s2", State: "running"})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	// original slice must be untouched (no aliasing).
	if len(rows) != 1 {
		t.Fatalf("original rows mutated: len = %d, want 1", len(rows))
	}
}

func TestUpsertSession_ReplacesExisting(t *testing.T) {
	rows := []TopSessionRow{
		{SessionID: "s1", State: "running"},
		{SessionID: "s2", State: "running"},
	}
	got := UpsertSession(rows, TopSessionRow{SessionID: "s1", State: "stalled"})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	for _, r := range got {
		if r.SessionID == "s1" && r.State != "stalled" {
			t.Fatalf("s1 not replaced: %+v", r)
		}
	}
}
