package jobs

// Purpose: the production lease constructors refuse a missing part, so no
//
//	composed LeaseManager journals into nothing (AUD-043 item 1).
//
// SPORT: jobs/lease-model (ADD, P1-CAP-10).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestLeaseManagerRequiresEventSink: NewLeaseEventSink refuses each nil
// part and NewLeaseManagerWithEvents refuses a nil sink, both
// KindInvalidInput; a complete sink journals a real acquire.
func TestLeaseManagerRequiresEventSink(t *testing.T) {
	_, bus, j, attn := newTestSink(t)
	for name, build := range map[string]func() (*LeaseEventSink, error){
		"bus":       func() (*LeaseEventSink, error) { return NewLeaseEventSink(nil, j, attn) },
		"journal":   func() (*LeaseEventSink, error) { return NewLeaseEventSink(bus, nil, attn) },
		"attention": func() (*LeaseEventSink, error) { return NewLeaseEventSink(bus, j, nil) },
	} {
		if sink, err := build(); sink != nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("NewLeaseEventSink without %s = %v, %v; want KindInvalidInput", name, sink, err)
		}
	}
	store, clock := newTestStore(t), newTestClock()
	lm, err := NewLeaseManagerWithEvents(store, clock, alwaysController, DefaultLeaseDefaults(), nil)
	if lm != nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("NewLeaseManagerWithEvents(nil sink) = %v, %v; want KindInvalidInput", lm, err)
	}
	sink, err := NewLeaseEventSink(bus, j, attn)
	if err != nil {
		t.Fatalf("NewLeaseEventSink: %v", err)
	}
	if lm, err = NewLeaseManagerWithEvents(store, clock, alwaysController, DefaultLeaseDefaults(), sink); err != nil {
		t.Fatalf("NewLeaseManagerWithEvents: %v", err)
	}
	got, err := lm.Acquire(context.Background(), "repo-ctor", "x/**", "job-ctor")
	if err != nil || !got.Granted {
		t.Fatalf("Acquire = %+v, %v", got, err)
	}
	entries, err := j.Replay(context.Background(), leaseEntityID("repo-ctor", got.Lease.ScopeGlob), journal.Cursor{}, nil)
	if err != nil || len(entries) != 1 || entries[0].Kind != journal.KindIntent {
		t.Fatalf("journal after acquire = %+v, %v; want one intent entry", entries, err)
	}
}
