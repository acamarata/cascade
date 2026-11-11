package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func adapterFixture(t *testing.T) (fleetTopologyStore, topology.Lane, *secrets.Detector) {
	t.Helper()
	home := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(name, home)
	}
	db, err := sql.Open("sqlite", filepath.Join(home, "topology.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := testkit.NewFrozenClock(at)
	if err := topology.ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatal(err)
	}
	s := fleetTopologyStore{Store: topology.NewStore(db), Quota: topology.NewQuotaStore(db)}
	ctx := context.Background()
	lane := topology.Lane{ID: "lane-a", RuntimeProfileRef: "profile-a", QuotaDomainRef: "domain-a", ModelID: "model-a", Effort: topology.EffortNA, LaneClass: topology.LaneClassUnranked, InteractionClass: topology.InteractionInteractive, Health: topology.LaneHealthAvailable, BaseShadowPrice: 1.25, DiscoveredAt: at, OfferingSnapshot: topology.OfferingSnapshot{ProviderPolicy: topology.ProviderPolicy{Flags: map[string]string{"training": "false", "retention": "30d"}}}}
	steps := []error{
		s.UpsertAccount(ctx, topology.Account{ID: "account-a", Provider: "provider-x", Billing: topology.BillingInfo{Kind: topology.BillingAPI}, Role: topology.AccountRoleWorkforce}),
		s.UpsertQuotaDomain(ctx, topology.QuotaDomain{ID: "domain-a", AccountRef: "account-a", Kind: topology.QuotaDomainAPIProject, BillingTier: topology.BillingTierPaid}),
		s.UpsertRuntimeProfile(ctx, topology.RuntimeProfile{ID: "profile-a", Runtime: topology.RuntimeAPI, Persistent: true, Endpoint: topology.Endpoint{Host: "api.invalid", Port: 443}}),
		s.UpsertLane(ctx, lane),
		s.UpsertCredential(ctx, topology.Credential{ID: "holder-a", AccountRef: "account-a", QuotaDomainRef: "domain-a", RuntimeProfileRef: "profile-a", Health: topology.CredentialOK}),
		s.Quota.UpsertBucket(ctx, "domain-a", topology.DimensionRPM, topology.Bucket{Name: "rpm", Limit: 100, RemainingFraction: .75, Confidence: 1, Source: topology.SourceProviderStatus, Window: topology.BucketWindowRolling, ObservedAt: at, ResetAt: at.Add(time.Minute)}),
	}
	for _, err := range steps {
		if err != nil {
			t.Fatal(err)
		}
	}
	detector, err := secrets.ProcessDetector()
	if err != nil {
		t.Fatal(err)
	}
	return s, lane, detector
}

func adapterCeiling(t *testing.T, s fleetTopologyStore) func(context.Context, topology.LaneID, bool) (topology.Ceiling, error) {
	t.Helper()
	oldTable, oldPaid := topology.SensitivityTable, topology.PaidEvidenceCurrent
	topology.SensitivityTable = func(tier provider.SensitivityTier, _ topology.LaneKind, _ bool) bool {
		return tier == provider.SensitivityPublic || tier == provider.SensitivityInternal
	}
	topology.PaidEvidenceCurrent = func(context.Context, *topology.Store, topology.QuotaDomain) (bool, error) { return true, nil }
	t.Cleanup(func() { topology.SensitivityTable, topology.PaidEvidenceCurrent = oldTable, oldPaid })
	return s.CeilingForLane
}

func TestExplainLaneAdapter(t *testing.T) {
	s, lane, detector := adapterFixture(t)
	d := fleetTopologyDeps(s, adapterCeiling(t, s), detector)
	got, err := d.Lane(context.Background(), string(lane.ID))
	if err != nil {
		t.Fatal(err)
	}
	want := rpc.LaneDTO{ID: "lane-a", RuntimeProfile: "profile-a", QuotaDomain: "domain-a", ModelID: "model-a", Effort: string(lane.Effort), LaneClass: string(lane.LaneClass), InteractionClass: string(lane.InteractionClass), Health: string(lane.Health), HealthReason: "stored lane health: available", BaseShadowPrice: 1.25, Ceiling: "internal", CeilingReason: "restricted-permit", DiscoveredAt: lane.DiscoveredAt}
	if !got.Lane.DiscoveredAt.Equal(want.DiscoveredAt) {
		t.Fatalf("discovered_at = %v; want %v", got.Lane.DiscoveredAt, want.DiscoveredAt)
	}
	want.DiscoveredAt = got.Lane.DiscoveredAt
	if !reflect.DeepEqual(got.Lane, want) {
		t.Fatalf("lane = %+v; want %+v", got.Lane, want)
	}
	if got.Profile != (rpc.ProfileDTO{ID: "profile-a", Runtime: "api", Persistent: true}) {
		t.Fatalf("profile = %+v", got.Profile)
	}
	buckets, err := s.Quota.ListBuckets(context.Background(), "domain-a")
	if err != nil {
		t.Fatal(err)
	}
	b := buckets[0]
	wantDomain := rpc.DomainDTO{ID: "domain-a", Kind: string(topology.QuotaDomainAPIProject), BillingTier: "paid", Holders: 1, Buckets: []rpc.BucketDTO{{Name: b.Name, Window: string(b.Window), Source: string(b.Source), Limit: b.Limit, RemainingFraction: b.RemainingFraction, Confidence: b.Confidence, ResetAt: b.ResetAt, ObservedAt: b.ObservedAt}}}
	if !reflect.DeepEqual(got.Domain, wantDomain) {
		t.Fatalf("domain = %+v; want %+v", got.Domain, wantDomain)
	}
	payload, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, term := range []string{"price_term", "utility", "credential", "secret", "shadow_price_term"} {
		if strings.Contains(string(payload), term) {
			t.Fatalf("unexpected field %q", term)
		}
	}
	if err := d.Scan(payload); err != nil {
		t.Fatal(err)
	}
}

func TestLaneDTOCeilingUnavailableOnError(t *testing.T) {
	s, lane, detector := adapterFixture(t)
	d := fleetTopologyDeps(s, adapterCeiling(t, s), detector)
	lane.ID, lane.QuotaDomainRef = "lane-missing", "domain-missing"
	if err := s.UpsertLane(context.Background(), lane); err != nil {
		t.Fatal(err)
	}
	lanes, err := d.Lanes(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(lanes) != 2 {
		t.Fatalf("lanes = %d", len(lanes))
	}
	if lanes[0].Ceiling != "internal" || lanes[1].Ceiling != "unavailable" || lanes[1].CeilingReason != "not-found" {
		t.Fatalf("ceilings = %+v", lanes)
	}
}

func TestPublicDTOsCarryNoCredentialFields(t *testing.T) {
	testkit.RequireCredentialFreeDTO(t, rpc.LaneDTO{}, rpc.ProfileDTO{}, rpc.DomainDTO{}, rpc.BucketDTO{}, rpc.LaneExplainDTO{})
}

func TestTopologyAdapterLookups(t *testing.T) {
	s, lane, detector := adapterFixture(t)
	d := fleetTopologyDeps(s, adapterCeiling(t, s), detector)
	ctx := context.Background()
	domains, err := d.Domains(ctx)
	if err != nil || len(domains) != 1 || len(domains[0].Buckets) != 1 || domains[0].Holders != 1 {
		t.Fatalf("domains = %+v, %v", domains, err)
	}
	domain, err := d.Domain(ctx, "domain-a")
	if err != nil || !reflect.DeepEqual(domain, domains[0]) {
		t.Fatalf("domain = %+v, %v", domain, err)
	}
	if _, err := d.Lane(ctx, "missing"); err == nil {
		t.Fatal("missing lane accepted")
	}
	if _, err := d.Domain(ctx, "missing"); err == nil {
		t.Fatal("missing domain accepted")
	}
	if err := s.RetireLane(ctx, lane.ID, lane.DiscoveredAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	active, err := d.Lanes(ctx, false)
	if err != nil || len(active) != 0 {
		t.Fatalf("active = %+v, %v", active, err)
	}
	all, err := d.Lanes(ctx, true)
	if err != nil || len(all) != 1 || !all[0].Retired {
		t.Fatalf("all = %+v, %v", all, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := d.Domains(canceled); err == nil {
		t.Fatal("canceled read accepted")
	}
}

func TestTopologyAdapterRefusesUnavailableInputs(t *testing.T) {
	s, _, detector := adapterFixture(t)
	ctx := context.Background()
	for _, tc := range []struct {
		ceiling fleetTopologyCeiling
		reason  string
	}{
		{nil, "unavailable"},
		{func(context.Context, topology.LaneID, bool) (topology.Ceiling, error) {
			return topology.Ceiling{}, errors.New("read failed")
		}, "unavailable"},
		{func(context.Context, topology.LaneID, bool) (topology.Ceiling, error) {
			return topology.Ceiling{}, cascade.New(cascade.KindPermissionDenied, "read refused")
		}, "permission-denied"},
		{func(context.Context, topology.LaneID, bool) (topology.Ceiling, error) {
			return topology.Ceiling{Tier: provider.SensitivityTier(255), Reason: topology.CeilingLaneType}, nil
		}, "invalid-input"},
	} {
		d := fleetTopologyDeps(s, tc.ceiling, detector)
		lanes, err := d.Lanes(ctx, false)
		if err != nil || len(lanes) != 1 {
			t.Fatalf("lanes = %+v, %v", lanes, err)
		}
		if lanes[0].Ceiling != "unavailable" || lanes[0].CeilingReason != tc.reason {
			t.Fatalf("ceiling = %+v", lanes[0])
		}
	}
	d := fleetTopologyDeps(s, nil, nil)
	if err := d.Scan([]byte(`{}`)); err == nil {
		t.Fatal("nil detector accepted")
	}
	d = fleetTopologyDeps(fleetTopologyStore{}, nil, detector)
	if _, err := d.Lanes(ctx, false); err == nil {
		t.Fatal("nil store accepted")
	}
}
