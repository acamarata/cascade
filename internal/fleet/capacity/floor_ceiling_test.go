package capacity

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ceilings is a cap over a fixed per-lane ceiling table; a missing lane errors.
func ceilings(m map[conductor.LaneID]provider.SensitivityTier) func(context.Context, conductor.LaneID, conductor.LaneType) (topology.Ceiling, error) {
	return func(_ context.Context, id conductor.LaneID, _ conductor.LaneType) (topology.Ceiling, error) {
		tier, ok := m[id]
		if !ok {
			return topology.Ceiling{}, errors.New("no ceiling row for " + string(id))
		}
		return topology.Ceiling{Tier: tier, Reason: topology.CeilingBillingCap}, nil
	}
}

func TestLeaseFloorRefusesLaneAboveCeiling(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	delete(f.snap.Lanes, "lane-t0")
	f.deps.Floors = []LaneFilter{NewCeilingFloor(ceilings(map[conductor.LaneID]provider.SensitivityTier{
		"lane-t1": provider.SensitivityPublic, "lane-t2": provider.SensitivityPublic,
	}))}
	f.rebuild()
	_, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1)
	requireSentinel(t, err, ErrNoCapableTier, "lane-ceiling", "lane-t1", "lane-t2")
	f.zeroRows()

	f.deps.Floors = []LaneFilter{NewCeilingFloor(ceilings(map[conductor.LaneID]provider.SensitivityTier{
		"lane-t1": provider.SensitivityInternal, "lane-t2": provider.SensitivityPublic,
	}))}
	f.rebuild()
	l, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1)
	if err != nil || l.LaneID != "lane-t1" {
		t.Fatalf("Lease = (%+v, %v), want lane-t1 (the only lane at or above internal)", l, err)
	}
}

func TestLeaseFloorReadErrorRefuses(t *testing.T) {
	slot := ProviderSlot{LaneID: "lane-x", LaneType: conductor.LaneExternalAPI}
	req := request()
	req.Sensitivity = provider.SensitivityPublic
	boom := func(context.Context, conductor.LaneID, conductor.LaneType) (topology.Ceiling, error) {
		return topology.Ceiling{Tier: provider.SensitivityPublic, Reason: topology.CeilingBillingCap}, errors.New("ceiling read failed")
	}
	fl := NewCeilingFloor(boom)
	if fl.Name() != "lane-ceiling" {
		t.Fatalf("Name = %q, want lane-ceiling", fl.Name())
	}
	if ok, why := fl.Admit(req, slot); ok || why != "ceiling read failed" {
		t.Fatalf("Admit on cap error = (%v, %q), want refusal with the error as reason", ok, why)
	}
	if ok, why := NewCeilingFloor(nil).Admit(req, slot); ok || why != "lane-ceiling: no cap" {
		t.Fatalf("nil cap Admit = (%v, %q), want refusal", ok, why)
	}
	zero := func(context.Context, conductor.LaneID, conductor.LaneType) (topology.Ceiling, error) {
		return topology.Ceiling{}, nil
	}
	if ok, _ := NewCeilingFloor(zero).Admit(req, slot); ok {
		t.Fatal("a zero Ceiling admitted a lane")
	}

	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	delete(f.snap.Lanes, "lane-t0")
	f.deps.Floors = []LaneFilter{NewCeilingFloor(boom)}
	f.rebuild()
	_, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1)
	requireSentinel(t, err, ErrNoCapableTier, "lane-ceiling: ceiling read failed")
	f.zeroRows()
}

// TestLeaseFloorUnreconciledLaneRefusesNoQuotaDomain: lanes the topology has
// no row for refuse ErrNoQuotaDomain (not a capability gap); once one removed
// lane is above its ceiling instead, the refusal is ErrNoCapableTier.
func TestLeaseFloorUnreconciledLaneRefusesNoQuotaDomain(t *testing.T) {
	absent := cascade.New(cascade.KindNotFound, "credential not found")
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	delete(f.snap.Lanes, "lane-t0")
	f.deps.Floors = []LaneFilter{NewCeilingFloor(func(context.Context, conductor.LaneID, conductor.LaneType) (topology.Ceiling, error) {
		return topology.Ceiling{}, absent
	})}
	f.rebuild()
	_, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1)
	requireSentinel(t, err, ErrNoQuotaDomain, "lane-ceiling", "lane-t1", "lane-t2", "credential not found")
	f.zeroRows()

	f.deps.Floors = []LaneFilter{NewCeilingFloor(func(_ context.Context, id conductor.LaneID, _ conductor.LaneType) (topology.Ceiling, error) {
		if id == "lane-t1" {
			return topology.Ceiling{}, absent
		}
		return topology.Ceiling{Tier: provider.SensitivityPublic, Reason: topology.CeilingBillingCap}, nil
	})}
	f.rebuild()
	_, err = f.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1)
	requireSentinel(t, err, ErrNoCapableTier, "lane-t1", "lane-t2", "billing-cap")
	if hasSentinel(err, ErrNoQuotaDomain) {
		t.Fatalf("mixed refusal = %v, want no ErrNoQuotaDomain while a lane sits above its ceiling", err)
	}
	f.zeroRows()
}

// claudeLane is the Claude harness agent lane: AgentLaneSlots names it
// "agent:<driver>" and ReconcileAgentLanes names its credential the same.
const claudeLane conductor.LaneID = "agent:" + conductor.LaneID(topology.RuntimeClaudeCLI)

// seedAgentLane writes the rows ReconcileAgentLanes writes for the claude-cli
// harness, the lane carrying a declared non-training offering policy (no
// exported writer replaces the snapshot of an existing lane), then runs
// ReconcileAgentLanes over them, which keeps the snapshot.
func seedAgentLane(ctx context.Context, t *testing.T, f *fx, s *topology.Store) {
	t.Helper()
	id := string(claudeLane)
	cred := topology.CredentialID(id)
	steps := []func() error{
		func() error {
			return s.UpsertAccount(ctx, topology.Account{ID: topology.AccountID(id), Provider: "agent",
				Billing: topology.BillingInfo{Kind: topology.BillingFree}, Role: topology.AccountRoleWorkforce})
		},
		func() error {
			return s.UpsertQuotaDomain(ctx, topology.QuotaDomain{ID: topology.DomainID(id), AccountRef: topology.AccountID(id),
				Kind: topology.QuotaDomainAPIProject, BillingTier: topology.BillingTierDiscover})
		},
		func() error {
			return s.UpsertRuntimeProfile(ctx, topology.RuntimeProfile{ID: topology.RuntimeProfileID(id),
				Runtime: topology.RuntimeClaudeCLI, Persistent: true})
		},
		func() error {
			return s.UpsertCredential(ctx, topology.Credential{ID: cred, AccountRef: topology.AccountID(id),
				QuotaDomainRef: topology.DomainID(id), RuntimeProfileRef: topology.RuntimeProfileID(id), Health: topology.CredentialOK})
		},
		func() error {
			return s.UpsertLane(ctx, topology.Lane{ID: topology.LaneIDFor(topology.RuntimeProfileID(id), topology.DomainID(id), "", "",
				topology.EffortNA, topology.InteractionInteractive), RuntimeProfileRef: topology.RuntimeProfileID(id),
				QuotaDomainRef: topology.DomainID(id), CredentialRef: &cred, Effort: topology.EffortNA,
				InteractionClass: topology.InteractionInteractive, Health: topology.LaneHealthAvailable,
				LaneClass: topology.LaneClassUnranked, BaseShadowPrice: 1, DiscoveredAt: f.clock.t,
				OfferingSnapshot: topology.OfferingSnapshot{ProviderPolicy: topology.ProviderPolicy{
					Flags: map[string]string{"training": "false", "retention": "30d"}}}})
		},
	}
	for _, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("seed agent lane: %v", err)
		}
	}
	recs := []topology.AgentLaneRecord{{LaneName: id, Harness: string(topology.RuntimeClaudeCLI)}}
	if _, err := topology.ReconcileAgentLanes(ctx, s, f.clock, recs); err != nil {
		t.Fatalf("ReconcileAgentLanes: %v", err)
	}
}

// agentTopology migrates the topology schema onto f's DB, seeds the
// agent:claude-cli harness lane and wires the two topology seams: the
// conductor's sensitivity table and a paid-evidence check that reads the
// stored evidence rows.
func agentTopology(t *testing.T, f *fx) *topology.Store {
	t.Helper()
	ctx := t.Context()
	if err := topology.ApplyMigrationSchema(ctx, f.db, migrate.SQLiteEmitter{}, f.clock, "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	s := topology.NewStore(f.db)
	seedAgentLane(ctx, t, f, s)
	prevTable, prevPaid := topology.SensitivityTable, topology.PaidEvidenceCurrent
	t.Cleanup(func() { topology.SensitivityTable, topology.PaidEvidenceCurrent = prevTable, prevPaid })
	topology.SensitivityTable = func(tier provider.SensitivityTier, k topology.LaneKind, permitted bool) bool {
		lt := conductor.LaneUnresolved
		if k == topology.LaneKindExternalAPI {
			lt = conductor.LaneExternalAPI
		}
		if k == topology.LaneKindControllerLocal {
			lt = conductor.LaneControllerLocal
		}
		return conductor.SensitivityAllows(tier, lt, permitted)
	}
	topology.PaidEvidenceCurrent = func(ctx context.Context, st *topology.Store, d topology.QuotaDomain) (bool, error) {
		ev, err := st.BillingEvidenceFor(ctx, d.ID)
		return len(ev) > 0, err
	}
	return s
}

func TestAgentDomainPromotionLiftsLeaseFloor(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-a", provider.SensitivityInternal)
	f.snap.Lanes[claudeLane] = ProviderSlot{LaneID: claudeLane, Posture: entitled(), Tier: TierZero,
		LaneType: conductor.LaneExternalAPI, ProfileRef: string(topology.RuntimeClaudeCLI), State: StateAvailable, UpdatedAt: f.clock.t}
	s := agentTopology(t, f)
	f.deps.Floors = []LaneFilter{NewCeilingFloor(RegistryLaneCeilings(s, conductor.RestrictedPermits{}))}
	f.rebuild()
	req := request()
	req.Sensitivity = provider.SensitivityInternal
	d := dispatchFor("job-a")
	d.AgentOnly = true

	_, err := f.fleet.Lease(t.Context(), req, d, 1)
	requireSentinel(t, err, ErrNoCapableTier, "lane-ceiling", string(claudeLane), "billing-cap")
	f.zeroRows()

	ev := topology.BillingEvidence{DomainID: topology.DomainID(claudeLane), PromotedBy: "operator", TestOutcome: topology.EvidenceOutcomePassed,
		Key:       topology.EvidenceKey{Provider: "agent", InstanceID: "claude-cli", Endpoint: "harness:claude-cli", AuthRef: "harness"},
		ProbeKind: "harness_call", ProbeStatus: 200, ProbeEndpoint: "harness:claude-cli", ObservedAt: f.clock.t}
	if err := s.PromoteDomain(t.Context(), topology.DomainID(claudeLane), []topology.BillingEvidence{ev}); err != nil {
		t.Fatalf("PromoteDomain: %v", err)
	}
	rows, err := s.BillingEvidenceFor(t.Context(), topology.DomainID(claudeLane))
	if err != nil || len(rows) != 1 {
		t.Fatalf("BillingEvidenceFor = (%d rows, %v), want the harness evidence row", len(rows), err)
	}
	l, err := f.fleet.Lease(t.Context(), req, d, 1)
	if err != nil || l.LaneID != claudeLane {
		t.Fatalf("Lease after PromoteDomain = (%+v, %v), want the claude agent lane", l, err)
	}
	if !strings.HasPrefix(string(l.LaneID), agentLanePrefix) {
		t.Fatalf("lane %q is not an agent lane", l.LaneID)
	}
}
