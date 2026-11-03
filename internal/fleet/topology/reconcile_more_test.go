package topology

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestDomainBillingMappingsCoverEveryKind(t *testing.T) {
	kinds := map[BillingKind]QuotaDomainKind{
		BillingPool: QuotaDomainSharedPool, BillingSubscription: QuotaDomainSubscriptionWindow,
		BillingAPI: QuotaDomainAPIProject, BillingFree: QuotaDomainAPIProject,
		BillingKind("future"): QuotaDomainAPIProject,
	}
	for b, want := range kinds {
		if got := domainKindForBilling(b); got != want {
			t.Errorf("domainKindForBilling(%q) = %q, want %q", b, got, want)
		}
	}
	ids := map[BillingKind]DomainID{
		BillingFree: "prov:api", BillingKind("future"): "prov:api",
		BillingSubscription: "prov:subscription", BillingPool: "pool:p1",
	}
	for b, want := range ids {
		if got := domainIDForBilling(b, "prov", "p1"); got != want {
			t.Errorf("domainIDForBilling(%q) = %q, want %q", b, got, want)
		}
	}
}

func TestWindowForKindDefaultsPerKind(t *testing.T) {
	want := map[QuotaDomainKind]BucketWindow{
		QuotaDomainAPIProject: BucketWindowDay, QuotaDomainSubscriptionWindow: BucketWindowMonth,
		QuotaDomainSharedPool: BucketWindowWeek, QuotaDomainKind("future"): BucketWindowRolling,
	}
	for kind, w := range want {
		if got := windowForKind(kind); got != w {
			t.Errorf("windowForKind(%q) = %q, want %q", kind, got, w)
		}
	}
}

func TestDerefTime(t *testing.T) {
	if got := derefTime(nil); !got.IsZero() {
		t.Fatalf("derefTime(nil) = %v, want zero", got)
	}
	now := time.Date(2026, 9, 1, 3, 4, 5, 0, time.UTC)
	if got := derefTime(&now); !got.Equal(now) {
		t.Fatalf("derefTime(&now) = %v, want %v", got, now)
	}
}

func TestUpsertLaneRejectsInvalidLane(t *testing.T) {
	s := seedTicketStore(t)
	err := s.UpsertLane(context.Background(), Lane{})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("UpsertLane(zero) = %v, want invalid-input", err)
	}
}

func TestRetireLaneErrorPaths(t *testing.T) {
	ctx, s := context.Background(), seedTicketStore(t)
	at := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	if err := s.RetireLane(ctx, "no-such-lane", at); !errors.Is(err, ErrTopologyNotFound) {
		t.Fatalf("RetireLane(missing) = %v, want ErrTopologyNotFound", err)
	}
	if err := s.CheckReservable(ctx, "no-such-lane"); !errors.Is(err, ErrTopologyNotFound) {
		t.Fatalf("CheckReservable(missing) = %v, want ErrTopologyNotFound", err)
	}
	records := []AgentLaneRecord{{LaneName: "lane-a", Harness: string(RuntimeCodex)}}
	if _, err := ReconcileAgentLanes(ctx, s, newTestClock(), records); err != nil {
		t.Fatal(err)
	}
	lanes, err := s.ListLanes(ctx, false)
	if err != nil || len(lanes) != 1 {
		t.Fatalf("ListLanes = %d, %v", len(lanes), err)
	}
	id := lanes[0].ID
	if err := s.RetireLane(ctx, id, at); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireLane(ctx, id, at); !errors.Is(err, ErrTopologyConflict) {
		t.Fatalf("second RetireLane = %v, want ErrTopologyConflict", err)
	}
	if err := s.CheckReservable(ctx, id); !errors.Is(err, ErrLaneRetired) {
		t.Fatalf("CheckReservable(retired) = %v, want ErrLaneRetired", err)
	}
}

func TestReconcileAgentLanesRefusesAPIHarnesses(t *testing.T) {
	for _, h := range []RuntimeKind{RuntimeAPI, RuntimeOllama} {
		ctx, s := context.Background(), seedTicketStore(t)
		n, err := ReconcileAgentLanes(ctx, s, newTestClock(), []AgentLaneRecord{{LaneName: "x", Harness: string(h)}})
		assertSeedInvariant(t, err, "not an agent harness")
		if n != 0 {
			t.Fatalf("%s harness reported %d lanes", h, n)
		}
	}
}

func TestReconcileAgentLaneMissingRowsAreNotFound(t *testing.T) {
	ctx, s := context.Background(), seedTicketStore(t)
	if err := reconcileAgentLane(ctx, s, "agent:ghost", newTestClock()); !errors.Is(err, ErrTopologyNotFound) {
		t.Fatalf("reconcileAgentLane(missing) = %v, want ErrTopologyNotFound", err)
	}
}

func TestReconcileAgentLaneQuarantinesUnhealthyCredential(t *testing.T) {
	ctx, s := context.Background(), seedTicketStore(t)
	records := []AgentLaneRecord{{LaneName: "lane-a", Harness: string(RuntimeCodex)}}
	if _, err := ReconcileAgentLanes(ctx, s, newTestClock(), records); err != nil {
		t.Fatal(err)
	}
	mustExec(ctx, t, s, `UPDATE `+tableCredential+` SET quarantined_until = `+"9999999999999")
	if err := reconcileAgentLane(ctx, s, "agent:codex", newTestClock()); err != nil {
		t.Fatal(err)
	}
	lanes, err := s.ListLanes(ctx, false)
	if err != nil || len(lanes) != 1 || lanes[0].Health != LaneHealthQuarantined {
		t.Fatalf("lanes = %+v, %v; want one quarantined lane", lanes, err)
	}
}
