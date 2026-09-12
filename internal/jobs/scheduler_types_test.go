package jobs

// Purpose: Event union round-trip decode, unknown-kind fail-closed
//
//	behavior, FuzzScheduleEvent (the Event decoder never panics),
//	ScheduleDelta zero-value shape, and a reflection check that
//	Scheduler carries no mutable-state field.
//
// SPORT: jobs/scheduler/ADD (tests) (P1-E29-W6-S59-T5).

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestDecodeEvent_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   eventEnvelope
		want Event
	}{
		{"lease_acquired", eventEnvelope{Kind: "lease_acquired", JobID: "j1", LeaseID: "l1"}, LeaseAcquired{JobID: "j1", LeaseID: "l1"}},
		{"job_transitioned", eventEnvelope{Kind: "job_transitioned", JobID: "j1", From: "leased", To: "running"}, JobTransitioned{JobID: "j1", From: JobStateLeased, To: JobStateRunning}},
		{"cancel_requested", eventEnvelope{Kind: "cancel_requested", JobID: "j1"}, CancelRequested{JobID: "j1"}},
		{"lease_expired", eventEnvelope{Kind: "lease_expired", JobID: "j1", RepoID: "r1", ScopeGlob: "a/**"}, LeaseExpired{JobID: "j1", RepoID: "r1", ScopeGlob: "a/**"}},
		{"termination_confirmed", eventEnvelope{Kind: "termination_confirmed", JobID: "j1"}, TerminationConfirmed{JobID: "j1"}},
		{"lease_reclaimed", eventEnvelope{Kind: "lease_reclaimed", RepoID: "r1", ScopeGlob: "a/**", NewEpoch: 3}, LeaseReclaimed{RepoID: "r1", ScopeGlob: "a/**", NewEpoch: 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal envelope: %v", err)
			}
			got, err := DecodeEvent(data)
			if err != nil {
				t.Fatalf("DecodeEvent: %v", err)
			}
			if got != tc.want {
				t.Fatalf("DecodeEvent = %#v, want %#v", got, tc.want)
			}
			if got.eventKind() != tc.in.Kind {
				t.Fatalf("eventKind() = %q, want %q", got.eventKind(), tc.in.Kind)
			}
		})
	}
}

func TestDecodeEvent_UnknownKind(t *testing.T) {
	_, err := DecodeEvent([]byte(`{"kind":"not_a_real_event"}`))
	if err == nil {
		t.Fatal("DecodeEvent(unknown kind) = nil error, want KindInvalidInput")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("DecodeEvent(unknown kind) kind = %v, want KindInvalidInput", err)
	}
}

func TestDecodeEvent_MalformedJSON(t *testing.T) {
	_, err := DecodeEvent([]byte(`{not json`))
	if err == nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("DecodeEvent(malformed) = %v, want KindInvalidInput", err)
	}
}

// FuzzScheduleEvent proves the Event decoder never panics over arbitrary
// bytes (06 §5 rule 7). Seed corpus lives under
// testdata/fuzz/FuzzScheduleEvent/.
func FuzzScheduleEvent(f *testing.F) {
	f.Add([]byte(`{"kind":"lease_acquired","job_id":"j1","lease_id":"l1"}`))
	f.Add([]byte(`{"kind":"unknown"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))
	f.Add([]byte(`not json at all`))
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = DecodeEvent(data) // must never panic, whatever it returns
	})
}

func TestScheduleDelta_ZeroValue(t *testing.T) {
	var d ScheduleDelta
	if d.LeasesToAcquire != nil || d.JobsToAdvance != nil || d.OutboxIntents != nil || d.EventsToEmit != nil || d.Errors != nil {
		t.Fatalf("ScheduleDelta{} zero value has non-nil field: %#v", d)
	}
}

// TestScheduler_NoMutableState is a reflection check that Scheduler's
// only field is its injected Clock (an immutable constructor
// dependency), never a mutable field the "stateless" contract forbids
// (HOW 1: "Scheduler struct holds no mutable state").
func TestScheduler_NoMutableState(t *testing.T) {
	typ := reflect.TypeOf(Scheduler{})
	if typ.NumField() != 1 {
		t.Fatalf("Scheduler has %d fields, want exactly 1 (the injected clock)", typ.NumField())
	}
	if typ.Field(0).Name != "clock" {
		t.Fatalf("Scheduler's one field is %q, want \"clock\"", typ.Field(0).Name)
	}
}

func TestAttentionRaised_EventKind(t *testing.T) {
	if got := (AttentionRaised{JobID: "j1", Reason: "x"}).eventKind(); got != "attention_raised" {
		t.Fatalf("eventKind() = %q, want %q", got, "attention_raised")
	}
}

// alwaysHasEntriesReader and emptyReader are minimal Reader test doubles
// shared by scheduler_resume_test.go's staleness-scan tests.
type alwaysHasEntriesReader struct{}

func (alwaysHasEntriesReader) Replay(context.Context, string, int64) ([]JournalEntry, error) {
	return []JournalEntry{{EntityID: "x", Cursor: 1, Kind: "job_progress"}}, nil
}

type emptyReader struct{}

func (emptyReader) Replay(context.Context, string, int64) ([]JournalEntry, error) { return nil, nil }
