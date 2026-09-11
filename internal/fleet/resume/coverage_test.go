// Purpose: closes the coverage gap on defaults and small pure helpers
//   the other test files' scenarios do not happen to exercise: the
//   default WithPermitFn/JournalAppender New supplies when a caller
//   passes none, Classification.String, and the event-bus publish path.
// SPORT: internal.fleet.resume.ResumeManager/ADDED (tests) (P1-E13-W3-S27-T2).

package resume

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
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

// TestDefaultPermitAndAppender_UsedWhenNilSupplied drives a resumable
// fan-out cursor through a Manager built with withPermit=nil and
// appender=nil (New's defaulting path), and has the fanOut double call
// both seams directly — proving passthroughPermit runs fn and
// noopAppender.AppendLeg is a genuine no-op, neither a nil-panic.
func TestDefaultPermitAndAppender_UsedWhenNilSupplied(t *testing.T) {
	store, _, _ := newRealStore(t)
	seedFanOutCursor(t, store, "t-default-seams", 1)

	ran := false
	fo := func(ctx context.Context, _ provider.ModelRequest, _ int, _ map[int]conductor.JobID, withPermit conductor.WithPermitFn, appender conductor.JournalAppender) ([]provider.ModelResponse, error) {
		ran = true
		permitErr := withPermit(ctx, func(context.Context) error { return nil })
		if permitErr != nil {
			t.Fatalf("default withPermit: %v", permitErr)
		}
		if err := appender.AppendLeg(ctx, "fanout_leg_started", "t-default-seams", 0, nil); err != nil {
			t.Fatalf("default appender.AppendLeg: %v", err)
		}
		return []provider.ModelResponse{{JobID: "job-0"}}, nil
	}

	mgr, err := New(store, fo, nil, nil, nil, nil, "darwin")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := mgr.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !ran {
		t.Fatal("fanOut never ran through the default withPermit/appender seams")
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
	var calls []fakeFanOutCall
	mgr, err := New(store, fakeFanOut(&calls, nil, nil), nil, nil, nil, bus, "darwin")
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
	adapter := journalAppenderAdapter{journal: store, taskID: "t", attempt: 1}
	err := adapter.AppendLeg(context.Background(), "not-a-real-kind", "t", 0, nil)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("AppendLeg(unrecognized kind) = %v, want KindInvalidInput", err)
	}
}
