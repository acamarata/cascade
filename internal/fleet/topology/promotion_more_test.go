package topology

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/cascade"
)

func passingPosteriors(lane LaneID) *fakePosteriors {
	return &fakePosteriors{byLane: map[LaneID][]Posterior{
		lane: {{TaskClass: TaskClassClassify, Samples: 60, Mean: 0.9}},
	}}
}

func TestApplyPromotionsRefusesEachNilDependency(t *testing.T) {
	isolateHome(t)
	s := newTestStore(t)
	ctx, src, audit := context.Background(), &fakePosteriors{}, &recordingAuditor{}
	cases := map[string]func() error{
		"posterior source": func() error { _, e := s.ApplyPromotions(ctx, nil, newTestClock(), audit); return e },
		"clock":            func() error { _, e := s.ApplyPromotions(ctx, src, nil, audit); return e },
		"auditor":          func() error { _, e := s.ApplyPromotions(ctx, src, newTestClock(), nil); return e },
	}
	for name, call := range cases {
		err := call()
		if !errors.Is(err, ErrPromotionInput) || !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("nil %s: got %v, want ErrPromotionInput", name, err)
		}
	}
}

func TestApplyPromotionsAuditFailureRollsBackThePromotion(t *testing.T) {
	isolateHome(t)
	ctx, s := context.Background(), newTestStore(t)
	seedPromotionLane(t, s, "lane-a")
	auditErr := errors.New("audit sink down")
	_, err := s.ApplyPromotions(ctx, passingPosteriors("lane-a"), newTestClock(), &recordingAuditor{fail: auditErr})
	if !errors.Is(err, auditErr) || !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("audit failure = %v, want unavailable wrapping the sink error", err)
	}
	if c, p := laneColumnsRaw(t, s, "lane-a"); c != LaneClassUnranked || p != 1.0 {
		t.Fatalf("lane after failed audit = (%s, %v), want unranked/1 (rolled back)", c, p)
	}
}

func TestSetLaneClassIsCompareAndSet(t *testing.T) {
	isolateHome(t)
	ctx, s := context.Background(), newTestStore(t)
	seedPromotionLane(t, s, "lane-a")
	err := s.setLaneClass(ctx, "lane-a", LaneClassWorkerDedicated, LaneClassUnranked, 1)
	if !errors.Is(err, ErrTopologyConflict) {
		t.Fatalf("stale setLaneClass = %v, want ErrTopologyConflict", err)
	}
	err = s.setLaneClass(ctx, "no-such-lane", LaneClassUnranked, LaneClassWorkerDedicated, 2)
	if !errors.Is(err, ErrTopologyConflict) {
		t.Fatalf("setLaneClass on missing lane = %v, want ErrTopologyConflict", err)
	}
	if err := s.setLaneClass(ctx, "lane-a", LaneClassUnranked, LaneClassWorkerDedicated, 2); err != nil {
		t.Fatal(err)
	}
	if c, p := laneColumnsRaw(t, s, "lane-a"); c != LaneClassWorkerDedicated || p != 2 {
		t.Fatalf("lane = (%s, %v), want worker-dedicated/2", c, p)
	}
}

func TestLaneHealthForPrecedence(t *testing.T) {
	ok, bad := Credential{Health: CredentialOK}, Credential{Health: CredentialHealth("broken")}
	if got := laneHealthFor(registry.LaneState("available"), bad, QuotaDomain{}); got != LaneHealthQuarantined {
		t.Errorf("bad credential health = %q, want quarantined", got)
	}
	if got := laneHealthFor(registry.LaneState("available"), ok, QuotaDomain{Quarantined: true}); got != LaneHealthQuarantined {
		t.Errorf("quarantined domain health = %q, want quarantined", got)
	}
	if got := laneHealthFor(registry.LaneState("available"), ok, QuotaDomain{}); got != LaneHealthAvailable {
		t.Errorf("clean health = %q, want available", got)
	}
	if got := laneHealthFor(registry.LaneState("not-a-state"), ok, QuotaDomain{}); got != LaneHealthQuarantined {
		t.Errorf("unknown state health = %q, want quarantined", got)
	}
}
