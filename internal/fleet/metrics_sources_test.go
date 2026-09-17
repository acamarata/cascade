package fleet

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// Purpose (this file): where the counts COME FROM — the auto-advance
//   recorder's verdicts and the attention topic's own event stream — kept
//   apart from metrics_test.go's counting rules for the 300-line cap.
//
// The two questions are genuinely different: metrics_test.go asks whether
//   a count is right once taken, and this file asks whether the right
//   things get counted at all. A redelivered event and an acknowledgement
//   are both events that must NOT count, and neither is a counting bug.
// SPORT: fleet.Metrics source tests (ADD) — P1-E18-W4-S40-T3.

// TestOnlyAnAutoApprovalCounts holds what "auto-resolved" measures. A
// refusal is the opposite of "resolved without user interruption", and a
// refusal is itself what PRODUCES an interruption — which the attention
// path counts, so counting it here too would double it.
func TestOnlyAnAutoApprovalCounts(t *testing.T) {
	m := newMetrics(t)
	decision := supervision.PolicyDecision{Level: policy.L0}
	for _, verdict := range []supervision.Verdict{
		supervision.VerdictCeilingRefused,
		supervision.VerdictUntrustedRefused,
		supervision.VerdictElevatedRefused,
		supervision.VerdictProfileDisabled,
		supervision.VerdictFailClosedDenied,
	} {
		m.RecordAutoAdvance(decision, verdict)
	}
	if got := m.AutoResolved(policy.L0); got != 0 {
		t.Fatalf("L0 = %d after five refusals, want 0", got)
	}
	m.RecordAutoAdvance(decision, supervision.VerdictAutoApproved)
	if got := m.AutoResolved(policy.L0); got != 1 {
		t.Errorf("L0 = %d after one approval, want 1", got)
	}
}

// TestAttentionEventsCountOncePerPromotion is the bus half, and the
// redelivery case with it: the bus replays from a durable cursor, so the
// same promotion can arrive twice and must not count twice.
func TestAttentionEventsCountOncePerPromotion(t *testing.T) {
	m := newMetrics(t)
	seen := &counted{}

	promotion := supervision.AttentionItem{
		ID: "item-1", Kind: supervision.KindPolicyAsk, SourceRef: "task-x",
	}
	ev := attentionEvent(t, promotion)
	m.countAttentionEvent(ev, seen)
	m.countAttentionEvent(ev, seen) // the redelivery
	m.countAttentionEvent(ev, seen)

	if got := m.Interruptions(); got != 1 {
		t.Errorf("fleet total = %d after one promotion delivered three times, want 1", got)
	}
	if n, _ := m.InterruptionsFor("task-x"); n != 1 {
		t.Errorf("task-x = %d, want 1", n)
	}
}

// TestAnAcknowledgementIsNotAnInterruption is the other half of "what
// counts". The topic carries every change, acknowledgements included, and
// counting those would double every interruption the moment a human dealt
// with it.
func TestAnAcknowledgementIsNotAnInterruption(t *testing.T) {
	m := newMetrics(t)
	seen := &counted{}
	acked := int64(1700000000000)
	m.countAttentionEvent(attentionEvent(t, supervision.AttentionItem{
		ID: "item-2", SourceRef: "task-y", AckedAt: &acked,
	}), seen)

	if got := m.Interruptions(); got != 0 {
		t.Errorf("fleet total = %d, want 0 — an ack is not an interruption", got)
	}
}

// TestAnUnreadableEventIsIgnored holds the rule that a metric must not
// take a daemon down, and must not invent a number either.
func TestAnUnreadableEventIsIgnored(t *testing.T) {
	m := newMetrics(t)
	seen := &counted{}
	m.countAttentionEvent(events.Event{
		Kind: supervision.ChangedKind, Payload: []byte("{not json"),
	}, seen)
	m.countAttentionEvent(events.Event{
		Kind: "some.other.kind", Payload: []byte(`{"id":"x","source_ref":"t"}`),
	}, seen)
	m.countAttentionEvent(attentionEvent(t, supervision.AttentionItem{SourceRef: "no-id"}), seen)

	if got := m.Interruptions(); got != 0 {
		t.Errorf("fleet total = %d, want 0", got)
	}
}

// TestTheNamespaceMatchesTheRealPublisher keeps this package's constant
// from drifting from the topic the attention store actually publishes on.
// A consumer subscribed to the wrong namespace reads nothing forever and
// looks exactly like a quiet fleet.
func TestTheNamespaceMatchesTheRealPublisher(t *testing.T) {
	var published []string
	store := supervision.NewStore(
		storetest.NewMemStore(),
		runtime.NewFixedClock(time.Unix(1700000000, 0)),
		namespaceSpy{fn: func(ns string) { published = append(published, ns) }},
		func() string { return "item-ns-1" },
		0,
	)
	if _, err := store.Push(context.Background(), supervision.AttentionItem{
		Kind:      supervision.KindPolicyAsk,
		SourceRef: "task-ns",
		ScopeRef:  supervision.ScopeRef{Kind: scope.ScopeKindGlobal},
	}); err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(published) != 1 {
		t.Fatalf("the store published %d times on a push, want 1 — this test asserts nothing otherwise",
			len(published))
	}
	if published[0] != AttentionNamespace {
		t.Errorf("the store publishes on %q, this package subscribes to %q", published[0], AttentionNamespace)
	}
	// The Kind is the second half: it is exported by the publisher, so a
	// rename there breaks this compile rather than silently disconnecting
	// the counter.
	if !strings.HasPrefix(string(supervision.ChangedKind), AttentionNamespace) {
		t.Errorf("kind %q does not live under namespace %q", supervision.ChangedKind, AttentionNamespace)
	}
}

// namespaceSpy records the namespace a publish went to.
type namespaceSpy struct{ fn func(string) }

func (s namespaceSpy) Publish(_ context.Context, ns string, _ events.EventKind, _ string, _ []byte) (events.Event, error) {
	s.fn(ns)
	return events.Event{}, nil
}

// attentionEvent marshals item into the event the store would publish.
func attentionEvent(t *testing.T, item supervision.AttentionItem) events.Event {
	t.Helper()
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	return events.Event{Kind: supervision.ChangedKind, Payload: raw}
}

// TestConsumeStopsWhenTheContextEnds holds the loop's bound: it never
// blocks on anything but ctx and the subscription closing.
func TestConsumeStopsWhenTheContextEnds(t *testing.T) {
	m := newMetrics(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.ConsumeAttention(ctx, &events.Subscription{}); err != nil {
		t.Errorf("ConsumeAttention on a cancelled context: %v", err)
	}
	if err := m.ConsumeAttention(context.Background(), nil); err != nil {
		t.Errorf("ConsumeAttention with no subscription: %v", err)
	}
}
