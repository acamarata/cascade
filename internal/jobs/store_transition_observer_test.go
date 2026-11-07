package jobs

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

// transitionRecorder collects every Transition an observer receives.
type transitionRecorder struct{ got []Transition }

func (r *transitionRecorder) observe(_ context.Context, t Transition) { r.got = append(r.got, t) }

func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CASCADE_HOME", t.TempDir())
}

func TestPutTransitionNotifiesObserver(t *testing.T) {
	isolateHome(t)
	ctx := context.Background()
	s := newTestStore(t)
	j := baseJob("j1")
	j.ConsecutiveFailedAttempts = 2
	if err := s.PutJob(ctx, j); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	rec := &transitionRecorder{}
	s.SetTransitionObserver(rec.observe)
	if err := s.PutTransition(ctx, "j1", JobStateLeased, 5); err != nil {
		t.Fatalf("to leased: %v", err)
	}
	if err := s.PutTransition(ctx, "j1", JobStateRunning, 6); err != nil {
		t.Fatalf("to running: %v", err)
	}
	want := []Transition{
		{JobID: "j1", To: JobStateLeased, At: 5, ConsecutiveFailedAttempts: 2},
		{JobID: "j1", To: JobStateRunning, At: 6, ConsecutiveFailedAttempts: 2},
	}
	if len(rec.got) != 2 || rec.got[0] != want[0] || rec.got[1] != want[1] {
		t.Fatalf("transitions = %+v, want %+v", rec.got, want)
	}

	rec.got = nil
	if err := s.afterTransition(ctx, "j1", JobStateAccepted); err != nil {
		t.Fatalf("afterTransition: %v", err)
	}
	if len(rec.got) != 1 || rec.got[0].To != JobStateAccepted {
		t.Fatalf("afterTransition accepted = %+v, want one", rec.got)
	}

	rec.got = nil
	if err := s.PutTransition(ctx, "j1", JobStatePending, 7); err == nil {
		t.Fatal("refused edge running->pending returned nil")
	}
	if len(rec.got) != 0 {
		t.Fatalf("refused edge notified %+v", rec.got)
	}

	s.SetTransitionObserver(nil)
	if err := s.PutTransition(ctx, "j1", JobStateCancelling, 8); err != nil {
		t.Fatalf("to cancelling: %v", err)
	}
	if len(rec.got) != 0 {
		t.Fatalf("nil observer still notified %+v", rec.got)
	}
}

func TestNotifyTransitionReadFailureGoesToSink(t *testing.T) {
	isolateHome(t)
	ctx := context.Background()
	s := newTestStore(t)
	rec := &transitionRecorder{}
	s.SetTransitionObserver(rec.observe)
	var sunk []error
	s.SetTerminalObserverErrorSink(func(err error) { sunk = append(sunk, err) })

	s.notifyTransition(ctx, "missing", JobStateRunning)
	if len(sunk) != 1 || len(rec.got) != 0 {
		t.Fatalf("missing row: sunk %v, calls %+v", sunk, rec.got)
	}
	if err := s.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	s.notifyTransition(ctx, "j1", JobStateRunning)
	if len(sunk) != 2 || len(rec.got) != 0 {
		t.Fatalf("read error: sunk %v, calls %+v", sunk, rec.got)
	}
}

func publishedKinds(t *testing.T, bus *events.Bus) ([]events.EventKind, []events.Event) {
	t.Helper()
	evs, err := bus.Replay(context.Background(), TransitionNamespace, 0)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	kinds := make([]events.EventKind, 0, len(evs))
	for _, ev := range evs {
		kinds = append(kinds, ev.Kind)
	}
	return kinds, evs
}

func TestPublishTransitionsKinds(t *testing.T) {
	isolateHome(t)
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1, 0))
	cases := []struct {
		to   JobState
		want []events.EventKind
	}{
		{JobStateRunning, []events.EventKind{rpc.EventJobTransitioned}},
		{JobStateLeased, []events.EventKind{rpc.EventJobTransitioned, rpc.EventJobLeased}},
		{JobStateAccepted, []events.EventKind{rpc.EventJobTransitioned, rpc.EventJobCompleted}},
		{JobStateRejected, []events.EventKind{rpc.EventJobTransitioned, rpc.EventJobFailed}},
		{JobStateFailed, []events.EventKind{rpc.EventJobTransitioned, rpc.EventJobFailed}},
	}
	for _, tc := range cases {
		bus := events.New(storetest.NewMemStore(), clock)
		obs := PublishTransitions(bus, func(err error) { t.Fatalf("sink: %v", err) })
		obs(ctx, Transition{JobID: "j1", To: tc.to, At: 7, ConsecutiveFailedAttempts: 2})
		kinds, evs := publishedKinds(t, bus)
		if len(kinds) != len(tc.want) {
			t.Fatalf("to %s kinds = %v, want %v", tc.to, kinds, tc.want)
		}
		for i := range kinds {
			if kinds[i] != tc.want[i] || evs[i].Source != "jobs.store" {
				t.Fatalf("to %s event %d = %s/%s, want %s", tc.to, i, kinds[i], evs[i].Source, tc.want[i])
			}
		}
		if tc.to == JobStateFailed {
			want := `{"job_id":"j1","to":"failed","at":7,"consecutive_failed_attempts":2}`
			for _, ev := range evs {
				if string(ev.Payload) != want {
					t.Fatalf("payload = %s, want %s", ev.Payload, want)
				}
			}
		}
	}

	bus := events.New(storetest.NewMemStore(), clock)
	if err := bus.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	var sunk []error
	PublishTransitions(bus, func(err error) { sunk = append(sunk, err) })(ctx, Transition{JobID: "j1", To: JobStateRunning})
	if len(sunk) != 1 {
		t.Fatalf("closed bus sink got %v, want one error", sunk)
	}
	PublishTransitions(bus, nil)(ctx, Transition{JobID: "j1", To: JobStateRunning})
	PublishTransitions(nil, func(err error) { sunk = append(sunk, err) })(ctx, Transition{JobID: "j1"})
	if len(sunk) != 2 {
		t.Fatalf("nil bus sink got %v, want two errors", sunk)
	}
}

func TestPublishTransitionsEndToEnd(t *testing.T) {
	isolateHome(t)
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.PutJob(ctx, baseJob("j1")); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	bus := events.New(storetest.NewMemStore(), testkit.NewFrozenClock(time.Unix(1, 0)))
	s.SetTransitionObserver(PublishTransitions(bus, nil))
	if err := s.PutTransition(ctx, "j1", JobStateLeased, 3); err != nil {
		t.Fatalf("to leased: %v", err)
	}
	_, evs := publishedKinds(t, bus)
	if len(evs) != 2 {
		t.Fatalf("events = %d, want 2", len(evs))
	}
	var got Transition
	if err := json.Unmarshal(evs[0].Payload, &got); err != nil || got.To != JobStateLeased || got.At != 3 {
		t.Fatalf("payload %s decode %v", evs[0].Payload, err)
	}
}
