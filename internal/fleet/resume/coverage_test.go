// Purpose: closes the coverage gap on small pure helpers the other test
//   files' scenarios do not happen to exercise: requeue's per-kind switch,
//   Classification.String, and the event-bus publish path.
// SPORT: internal.fleet.resume.ResumeManager/ADDED (tests) (P1-E13-W3-S27-T2).

package resume

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestClassificationString(t *testing.T) {
	cases := map[Classification]string{
		ClassResumable:      "resumable",
		ClassTerminal:       "terminal",
		ClassUnknownOutcome: "unknown-outcome",
		Classification(99):  "invalid",
	}
	for c, want := range cases {
		if got := c.String(); got != want {
			t.Errorf("Classification(%d).String() = %q, want %q", c, got, want)
		}
	}
}

// TestRequeueByKind: a fan-out cursor is left alone (nothing appended to
// any entity), an intent is re-queued, and an unknown kind fails closed.
func TestRequeueByKind(t *testing.T) {
	store, _, _ := newRealStore(t)
	ctx := context.Background()
	mgr, err := New(store, nil, nil, "darwin")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := mgr.requeue(ctx, resumeCursor{TaskID: "t-fan", FanOutID: "t-fan", Kind: cursorFanOut, Legs: 2}); err != nil {
		t.Fatalf("requeue(fan-out) = %v, want nil", err)
	}
	if ents, _ := store.ListEntities(ctx); len(ents) != 0 {
		t.Fatalf("requeue(fan-out) wrote entities %v, want none", ents)
	}
	if err := mgr.requeue(ctx, resumeCursor{TaskID: "t-int", Kind: cursorIntent, ActionID: "a"}); err != nil || entryCount(t, store, "t-int") != 1 {
		t.Fatalf("requeue(intent) = %v, entries %d; want nil and one re-queued intent", err, entryCount(t, store, "t-int"))
	}
	if err := mgr.requeue(ctx, resumeCursor{Kind: cursorKind(99)}); err != ErrUnrecognizedShape { //nolint:errorlint // identity
		t.Fatalf("requeue(unknown kind) = %v, want ErrUnrecognizedShape", err)
	}
}

func TestPublish_NilBusIsNoop(_ *testing.T) {
	var m Manager // zero value: bus is nil
	m.publish(context.Background(), Outcome{EntityID: "x", Err: ErrTruncatedTail})
	// No panic is the assertion; a nil bus must never be dereferenced.
}

func TestPublish_WithBusPublishesTypedEvent(t *testing.T) {
	bus := &recordingBus{}
	store, _, _ := newRealStore(t)
	mgr, err := New(store, nil, bus, "darwin")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mgr.publish(context.Background(), Outcome{EntityID: "x", Classification: ClassTerminal, Err: ErrTruncatedTail})
	if len(bus.published) != 1 {
		t.Fatalf("published %d events, want 1", len(bus.published))
	}
}

type recordingBus struct {
	published []string
}

func (b *recordingBus) Publish(_ context.Context, _, kind, _ string, payload []byte) error {
	b.published = append(b.published, kind+":"+string(payload))
	return nil
}

func TestJournalAppenderAdapter_UnrecognizedKindRefused(t *testing.T) {
	store, _, _ := newRealStore(t)
	adapter := &journalAppenderAdapter{journal: store}
	_, err := adapter.AppendLeg(context.Background(), "not-a-real-kind", "t", 0, nil)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("AppendLeg(unrecognized kind) = %v, want KindInvalidInput", err)
	}
}
