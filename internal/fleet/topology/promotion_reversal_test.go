package topology

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// promotedLane seeds one lane and promotes it to worker-fast through
// ApplyPromotions, returning the store, a settable clock and the auditor.
func promotedLane(t *testing.T, id LaneID) (*Store, *stepClock, *recordingAuditor, *fakePosteriors) {
	t.Helper()
	s := newTestStore(t)
	seedPromotionLane(t, s, id)
	clock := &stepClock{t: newTestClock().Now()}
	audit := &recordingAuditor{}
	src := &fakePosteriors{byLane: map[LaneID][]Posterior{
		id: {{TaskClass: TaskClassClassify, Samples: 90, Mean: 0.9, FamilyHint: LaneClassWorkerFast}},
	}}
	if _, err := s.ApplyPromotions(context.Background(), src, clock, audit); err != nil {
		t.Fatalf("promote %q: %v", id, err)
	}
	if c, _ := laneColumnsRaw(t, s, id); c != LaneClassWorkerFast {
		t.Fatalf("seed promotion: lane %q is %s, want worker-fast", id, c)
	}
	audit.events = nil
	return s, clock, audit, src
}

func reversalRows(t *testing.T, s *Store, id LaneID) []Reversal {
	t.Helper()
	rows, err := s.ListReversals(context.Background(), id)
	if err != nil {
		t.Fatalf("ListReversals: %v", err)
	}
	return rows
}

func TestPromotionReversalBoundedAndAudited(t *testing.T) {
	isolateHome(t)
	ctx := context.Background()
	const id LaneID = "lane-rev"
	s, clock, audit, src := promotedLane(t, id)
	fastPrice, _ := BaseShadowPrice(LaneClassWorkerFast)
	start := clock.t
	until := start.Add(24 * time.Hour)

	req := ReversalRequest{Lane: id, Class: LaneClassUnranked, Until: until, Reason: "regression seen"}
	if err := s.ReversePromotion(ctx, req, clock, audit); err != nil {
		t.Fatalf("ReversePromotion: %v", err)
	}
	if c, p := laneColumnsRaw(t, s, id); c != LaneClassUnranked || p != 1.0 {
		t.Fatalf("after reversal lane = (%s, %v), want (unranked, 1)", c, p)
	}
	if len(audit.events) != 1 || audit.events[0].Kind != PromotionEventReversed ||
		audit.events[0].FromClass != LaneClassWorkerFast || !audit.events[0].ExpiresAt.Equal(until) {
		t.Fatalf("reversal audit = %+v, want one reversed event from worker-fast", audit.events)
	}
	rows := reversalRows(t, s, id)
	if len(rows) != 1 || rows[0].PriorClass != LaneClassWorkerFast || rows[0].PriorPrice != fastPrice || rows[0].RestoredAt != nil {
		t.Fatalf("reversal rows = %+v", rows)
	}

	// Just before expiry the lane stays reversed even though its
	// posteriors would promote it again.
	clock.t = until.Add(-time.Millisecond)
	rep, err := s.ApplyPromotions(ctx, src, clock, audit)
	if err != nil || rep.RowsChanged != 0 || len(audit.events) != 1 {
		t.Fatalf("pre-expiry pass: rep=%+v err=%v events=%d", rep, err, len(audit.events))
	}
	if c, _ := laneColumnsRaw(t, s, id); c != LaneClassUnranked {
		t.Fatalf("pre-expiry lane = %s, want unranked", c)
	}

	checkReversalExpiry(t, s, clock, audit, src, id, until, fastPrice)
	t.Run("refusals", checkReversalRefusals)
}

// checkReversalExpiry runs the expiry half of
// TestPromotionReversalBoundedAndAudited.
func checkReversalExpiry(t *testing.T, s *Store, clock *stepClock, audit *recordingAuditor, src *fakePosteriors, id LaneID, until time.Time, fastPrice float64) {
	t.Helper()
	ctx := context.Background()
	// At expiry both prior values come back with one expiry event.
	clock.t = until
	rep, err := s.ApplyPromotions(ctx, src, clock, audit)
	if err != nil || !reflect.DeepEqual(rep.Restored, []LaneID{id}) || len(rep.Promoted) != 0 {
		t.Fatalf("expiry pass: rep=%+v err=%v", rep, err)
	}
	if c, p := laneColumnsRaw(t, s, id); c != LaneClassWorkerFast || p != fastPrice {
		t.Fatalf("after expiry lane = (%s, %v), want (worker-fast, %v)", c, p, fastPrice)
	}
	if len(audit.events) != 2 || audit.events[1].Kind != PromotionEventReversalExpiry || audit.events[1].ToClass != LaneClassWorkerFast {
		t.Fatalf("expiry audit = %+v, want one reversal-expired event", audit.events)
	}
	if rows := reversalRows(t, s, id); len(rows) != 1 || rows[0].RestoredAt == nil {
		t.Fatalf("reversal row not closed: %+v", rows)
	}
	clock.t = until.Add(time.Hour)
	rep, err = s.ApplyPromotions(ctx, src, clock, audit)
	if err != nil || rep.RowsChanged != 0 || len(audit.events) != 2 {
		t.Fatalf("repeat pass: rep=%+v err=%v events=%d, want zero rows", rep, err, len(audit.events))
	}
}

// checkReversalRefusals runs the refusal half of
// TestPromotionReversalBoundedAndAudited: each refused request leaves the
// lane columns, the reversal rows and the audit log unchanged.
func checkReversalRefusals(t *testing.T) {
	ctx := context.Background()
	const id LaneID = "lane-refuse"
	s, clock, audit, _ := promotedLane(t, id)
	now := clock.t
	fastPrice, _ := BaseShadowPrice(LaneClassWorkerFast)
	cases := []struct {
		name string
		req  ReversalRequest
		msg  string
	}{
		{"31 days", ReversalRequest{Lane: id, Class: LaneClassUnranked, Until: now.Add(31 * 24 * time.Hour)}, "longer than 30 days"},
		{"expired", ReversalRequest{Lane: id, Class: LaneClassUnranked, Until: now}, "already expired"},
		{"unknown class", ReversalRequest{Lane: id, Class: "not-a-class", Until: now.Add(time.Hour)}, "unknown reversal class"},
	}
	for _, c := range cases {
		assertSentinel(t, s.ReversePromotion(ctx, c.req, clock, audit), ErrPromotionInput, c.msg)
	}
	// The 30-day bound itself is allowed (paired positive case) on a
	// second lane, so the refusals above are the bound, not a blanket no.
	seedPromotionLane(t, s, "lane-ok")
	if _, err := s.db.ExecContext(ctx, `UPDATE `+tableLane+` SET lane_class = 'worker-fast' WHERE id = 'lane-ok'`); err != nil {
		t.Fatalf("set lane-ok class: %v", err)
	}
	okReq := ReversalRequest{Lane: "lane-ok", Class: LaneClassUnranked, Until: now.Add(MaxReversalWindow)}
	if err := s.ReversePromotion(ctx, okReq, clock, &recordingAuditor{}); err != nil {
		t.Fatalf("30-day reversal: %v", err)
	}

	// Injected audit failure rolls the whole reversal back.
	auditErr := cascade.New(cascade.KindUnavailable, "audit log offline")
	failing := &recordingAuditor{fail: auditErr}
	good := ReversalRequest{Lane: id, Class: LaneClassUnranked, Until: now.Add(time.Hour)}
	if err := s.ReversePromotion(ctx, good, clock, failing); err == nil || !chainHolds(err, auditErr) {
		t.Fatalf("audit failure: err = %v, want the audit error", err)
	}

	// An unreadable prior row is refused, never read as absence.
	if _, err := s.db.ExecContext(ctx, `UPDATE `+tableLane+` SET roles = 'not json' WHERE id = ?`, string(id)); err != nil {
		t.Fatalf("corrupt lane: %v", err)
	}
	if err := s.ReversePromotion(ctx, good, clock, audit); err == nil {
		t.Fatal("unreadable lane row: want an error")
	}

	if c, p := laneColumnsRaw(t, s, id); c != LaneClassWorkerFast || p != fastPrice {
		t.Fatalf("refusals changed lane to (%s, %v)", c, p)
	}
	if rows := reversalRows(t, s, id); len(rows) != 0 {
		t.Fatalf("refusals wrote reversal rows: %+v", rows)
	}
	if len(audit.events) != 0 {
		t.Fatalf("refusals emitted audit events: %+v", audit.events)
	}
}
