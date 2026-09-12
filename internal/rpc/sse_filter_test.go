package rpc

// Purpose: proves the six P1-E29-W6-S60-T1 job/lease SSE kinds
//	(sse_jobs.go) are correctly delivered/excluded by the REAL, unmodified
//	SSEHandler (sse.go) — no second filter parser exists for them (see
//	sse_jobs.go's CONTRACT NOTE for the exact-match-vs-glob deviation this
//	proves against). Uses sse_test.go's own established fixtures
//	(newTestBus/runSSE/mustPublish/waitFor), a real internal/events.Bus.
//
// SPORT: internal.rpc.SSEHandler/ADDED job/lease topics (P1-E29-W6-S60-T1).

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/testkit"
)

// newJobLeaseSSEHandler builds an SSEHandler whose known predicate is
// CombineKnownEventKind(knownAB, KnownJobLeaseEventKind) — proving a
// composition root can OR this ticket's six kinds onto whatever predicate
// it already had, per sse_jobs.go's doc comment.
func newJobLeaseSSEHandler(t *testing.T) (*SSEHandler, *events.Bus, *testkit.FrozenClock) {
	t.Helper()
	bus, clock := newTestBus()
	t.Cleanup(func() { _ = bus.Close() })
	known := CombineKnownEventKind(knownAB, KnownJobLeaseEventKind)
	return NewSSEHandler(bus, "ns", known, clock), bus, clock
}

func TestSSEFilter_JobTopicExcludesLeaseEvents(t *testing.T) {
	h, bus, _ := newJobLeaseSSEHandler(t)

	ctx, cancel := context.WithCancel(context.Background())
	w, done := runSSE(ctx, h, "?filter=job.leased,job.transitioned,job.completed,job.failed", "")
	waitFor(t, func() bool { _, _, wrote := w.snapshot(); return wrote })

	mustPublish(t, bus, "ns", EventJobLeased, "1")
	mustPublish(t, bus, "ns", EventLeaseAcquiredSSE, "2")
	waitFor(t, func() bool { _, body, _ := w.snapshot(); return strings.Contains(body, `"kind":"job.leased"`) })

	cancel()
	<-done
	_, body, _ := w.snapshot()
	if strings.Contains(body, `"kind":"lease.acquired"`) {
		t.Fatalf("filter=job.* must exclude lease.* events, got body: %s", body)
	}
}

func TestSSEFilter_LeaseTopicExcludesJobEvents(t *testing.T) {
	h, bus, _ := newJobLeaseSSEHandler(t)

	ctx, cancel := context.WithCancel(context.Background())
	w, done := runSSE(ctx, h, "?filter=lease.acquired,lease.expired", "")
	waitFor(t, func() bool { _, _, wrote := w.snapshot(); return wrote })

	mustPublish(t, bus, "ns", EventLeaseAcquiredSSE, "1")
	mustPublish(t, bus, "ns", EventJobCompleted, "2")
	waitFor(t, func() bool { _, body, _ := w.snapshot(); return strings.Contains(body, `"kind":"lease.acquired"`) })

	cancel()
	<-done
	_, body, _ := w.snapshot()
	if strings.Contains(body, `"kind":"job.completed"`) {
		t.Fatalf("filter=lease.* must exclude job.* events, got body: %s", body)
	}
}

func TestSSEFilter_EmptyFilterDeliversEveryRegisteredKind(t *testing.T) {
	h, bus, _ := newJobLeaseSSEHandler(t)

	ctx, cancel := context.WithCancel(context.Background())
	w, done := runSSE(ctx, h, "", "")
	waitFor(t, func() bool { _, _, wrote := w.snapshot(); return wrote })

	mustPublish(t, bus, "ns", EventJobLeased, "1")
	mustPublish(t, bus, "ns", EventLeaseExpiredSSE, "2")
	waitFor(t, func() bool { _, body, _ := w.snapshot(); return strings.Contains(body, `"kind":"lease.expired"`) })

	cancel()
	<-done
	_, body, _ := w.snapshot()
	if !strings.Contains(body, `"kind":"job.leased"`) || !strings.Contains(body, `"kind":"lease.expired"`) {
		t.Fatalf("empty filter (subscribe-all) must deliver every registered kind, got: %s", body)
	}
}

func TestSSEFilter_MalformedTopicStill400(t *testing.T) {
	h, _, _ := newJobLeaseSSEHandler(t)

	w, done := runSSE(context.Background(), h, "?filter=job.leased,not.a.real.topic", "")
	<-done

	status, body, _ := w.snapshot()
	if status != 400 {
		t.Fatalf("status = %d, want 400 (K/S-23.T3's malformed-filter path must still fire)", status)
	}
	if !strings.Contains(body, "not.a.real.topic") {
		t.Fatalf("body must name the rejected topic, got: %s", body)
	}
}

func TestKnownJobLeaseEventKind_RecognizesExactlySixKinds(t *testing.T) {
	for _, k := range jobLeaseEventKinds {
		if !KnownJobLeaseEventKind(k) {
			t.Errorf("KnownJobLeaseEventKind(%q) = false, want true", k)
		}
	}
	if KnownJobLeaseEventKind("job.leased.bogus") {
		t.Error("KnownJobLeaseEventKind must not match an unregistered kind")
	}
}
