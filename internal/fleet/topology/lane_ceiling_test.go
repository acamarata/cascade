package topology

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

var updateCeilingGolden = flag.Bool("update-ceiling-golden", false, "rewrite testdata/lane_ceiling_defaults.golden.json")

// NewCeilingTestStore returns a migrated store for the external tests.
func NewCeilingTestStore(t *testing.T) *Store { return newTestStore(t) }

// SetPaidEvidenceForTest swaps the evidence seam for one test.
func SetPaidEvidenceForTest(t *testing.T, fn func(context.Context, *Store, QuotaDomain) (bool, error)) {
	t.Helper()
	prev := PaidEvidenceCurrent
	PaidEvidenceCurrent = fn
	t.Cleanup(func() { PaidEvidenceCurrent = prev })
}

// SeedCeilingLane writes one account, domain, persistent profile (host as
// its endpoint, "" for none), credential name and lane with policy flags.
func SeedCeilingLane(t *testing.T, s *Store, name string, tier BillingTier, rt RuntimeKind, host string, flags map[string]string) LaneID {
	t.Helper()
	ctx := context.Background()
	acct, domain := AccountID("acct-"+name), DomainID(name+":api")
	profile := RuntimeProfile{ID: RuntimeProfileID("prof-" + name), Runtime: rt, Persistent: true}
	cred := CredentialID(name)
	steps := []func() error{
		func() error {
			return s.UpsertAccount(ctx, Account{ID: acct, Provider: "openai_compat", Billing: BillingInfo{Kind: BillingAPI}, Role: AccountRoleWorkforce})
		},
		func() error {
			return s.UpsertQuotaDomain(ctx, QuotaDomain{ID: domain, AccountRef: acct, Kind: QuotaDomainAPIProject, BillingTier: tier})
		},
		func() error { return s.UpsertRuntimeProfile(ctx, profile) },
		func() error {
			ep := Endpoint{Host: host}
			if host != "" && !strings.HasPrefix(host, "unix:") {
				ep.Port = 8080
			}
			return s.SetEndpoint(ctx, profile.ID, ep)
		},
		func() error {
			return s.UpsertCredential(ctx, Credential{ID: cred, AccountRef: acct, QuotaDomainRef: domain,
				SecretRef: VaultKeyRef("provider." + name + ".key"), RuntimeProfileRef: profile.ID, Health: CredentialOK})
		},
	}
	for _, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	id := LaneIDFor(profile.ID, domain, "", "", EffortNA, InteractionInteractive)
	lane := Lane{ID: id, RuntimeProfileRef: profile.ID, QuotaDomainRef: domain, CredentialRef: &cred, Effort: EffortNA,
		InteractionClass: InteractionInteractive, Health: LaneHealthAvailable, LaneClass: LaneClassUnranked,
		BaseShadowPrice: 1, DiscoveredAt: newTestClock().Now(),
		OfferingSnapshot: OfferingSnapshot{ProviderPolicy: ProviderPolicy{Flags: flags}}}
	if err := s.UpsertLane(ctx, lane); err != nil {
		t.Fatalf("UpsertLane: %v", err)
	}
	return id
}

var knownPolicy = map[string]string{"training": "false", "retention": "30d"}

func setTier(t *testing.T, s *Store, id DomainID, tier BillingTier) {
	t.Helper()
	d, err := s.GetQuotaDomain(context.Background(), id)
	if err != nil {
		t.Fatalf("GetQuotaDomain: %v", err)
	}
	d.BillingTier = tier
	if err := s.UpsertQuotaDomain(context.Background(), d); err != nil {
		t.Fatalf("UpsertQuotaDomain: %v", err)
	}
}

// TestLaneCeilingBillingCap: free and discover cap every lane at public; a
// paid domain whose stored key matches the current key reads internal; a
// changed key or an unset seam keeps public; a seam error refuses.
func TestLaneCeilingBillingCap(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	remote := SeedCeilingLane(t, s, "gw", BillingTierFree, RuntimeAPI, "api.example.com", knownPolicy)
	local := SeedCeilingLane(t, s, "loc", BillingTierDiscover, RuntimeOllama, "127.0.0.1", knownPolicy)
	stored, current := map[DomainID]string{"gw:api": "k1"}, "k1"
	SetPaidEvidenceForTest(t, func(_ context.Context, _ *Store, d QuotaDomain) (bool, error) {
		return stored[d.ID] == current, nil
	})
	for _, id := range []LaneID{remote, local} {
		c, err := s.CeilingForLane(ctx, id, false)
		if err != nil || c.Tier != provider.SensitivityPublic || c.Reason != CeilingBillingCap {
			t.Fatalf("unpaid lane %s = %+v, %v; want public/billing-cap", id, c, err)
		}
	}
	setTier(t, s, "gw:api", BillingTierPaid)
	c, err := s.CeilingForLane(ctx, remote, false)
	if err != nil || c.Tier != provider.SensitivityInternal || c.Reason != CeilingRestrictedPermit || c.BillingTier != BillingTierPaid {
		t.Fatalf("promoted lane = %+v, %v; want internal", c, err)
	}
	current = "k2"
	if c, _ := s.CeilingForLane(ctx, remote, false); c.Tier != provider.SensitivityPublic || c.Reason != CeilingBillingCap {
		t.Fatalf("stale key lane = %+v, want public/billing-cap", c)
	}
	SetPaidEvidenceForTest(t, nil)
	if c, _ := s.CeilingForLane(ctx, remote, false); c.Tier != provider.SensitivityPublic {
		t.Fatalf("nil key fn lane = %+v, want public", c)
	}
	SetPaidEvidenceForTest(t, func(context.Context, *Store, QuotaDomain) (bool, error) { return false, errors.New("boom") })
	if _, err := s.CeilingForLane(ctx, remote, false); err == nil {
		t.Fatal("evidence error read as no cap")
	}
}

type ceilingRow struct {
	Billing   BillingTier `json:"billing"`
	LaneType  LaneKind    `json:"lane_type"`
	Permitted bool        `json:"permitted"`
	Offering  string      `json:"offering"`
	Tier      string      `json:"tier,omitempty"`
	Reason    string      `json:"reason,omitempty"`
	Refused   bool        `json:"refused,omitempty"`
}

// TestLaneCeilingDefaultsGolden pins every billing x type x permit x
// offering row of the pure rule with current paid evidence.
func TestLaneCeilingDefaultsGolden(t *testing.T) {
	offerings := map[string]map[string]string{"known": knownPolicy, "unknown": nil}
	var rows []ceilingRow
	for _, b := range []BillingTier{BillingTierFree, BillingTierPaid, BillingTierDiscover} {
		for _, lt := range []LaneKind{LaneKindControllerLocal, LaneKindExternalAPI, LaneKindUnresolved} {
			for _, p := range []bool{false, true} {
				for _, o := range []string{"known", "unknown"} {
					row := ceilingRow{Billing: b, LaneType: lt, Permitted: p, Offering: o}
					c, err := computeCeiling(lt, p, QuotaDomain{ID: "d", BillingTier: b}, true, ProviderPolicy{Flags: offerings[o]})
					if err != nil {
						row.Refused = true
					} else {
						row.Tier, row.Reason = c.Tier.String(), string(c.Reason)
					}
					rows = append(rows, row)
				}
			}
		}
	}
	got, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "lane_ceiling_defaults.golden.json")
	if *updateCeilingGolden {
		if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(want) != string(got)+"\n" {
		t.Fatalf("ceiling rows drifted from %s:\n%s", path, got)
	}
}

// TestGatewayTrainingUnknownCapsPublic: a paid external gateway whose
// training term is unknown, true or missing a retention term reads public.
func TestGatewayTrainingUnknownCapsPublic(t *testing.T) {
	d := QuotaDomain{ID: "gw", BillingTier: BillingTierPaid}
	for _, flags := range []map[string]string{nil, {"training": "unknown", "retention": "30d"},
		{"training": "true", "retention": "30d"}, {"training": "false"}} {
		c, err := computeCeiling(LaneKindExternalAPI, true, d, true, ProviderPolicy{Flags: flags})
		if err != nil || c.Tier != provider.SensitivityPublic || c.Reason != CeilingOfferingPolicy {
			t.Errorf("flags %v = %+v, %v; want public/offering-policy", flags, c, err)
		}
	}
	if c, _ := computeCeiling(LaneKindExternalAPI, true, d, true, ProviderPolicy{Flags: knownPolicy}); c.Tier != provider.SensitivityRestricted {
		t.Errorf("known no-training gateway = %+v, want restricted", c)
	}
}

// TestLaneCeilingLocality: only a controller-local endpoint reaches
// local-only; an empty endpoint is remote.
func TestLaneCeilingLocality(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	SetPaidEvidenceForTest(t, func(context.Context, *Store, QuotaDomain) (bool, error) { return true, nil })
	for host, want := range map[string]provider.SensitivityTier{
		"127.0.0.1": provider.SensitivityLocalOnly, "unix:/run/x.sock": provider.SensitivityLocalOnly,
		"": provider.SensitivityRestricted, "api.example.com": provider.SensitivityRestricted,
	} {
		name := "h" + filepath.Base(host)
		if host == "" {
			name = "hempty"
		}
		id := SeedCeilingLane(t, s, name, BillingTierPaid, RuntimeAPI, host, knownPolicy)
		c, err := s.CeilingForLane(ctx, id, true)
		if err != nil || c.Tier != want {
			t.Errorf("host %q = %+v, %v; want %s", host, c, err, want)
		}
		if TierAdmits(provider.SensitivityLocalOnly, c) != (want == provider.SensitivityLocalOnly) {
			t.Errorf("host %q local-only admission wrong", host)
		}
	}
}

// TestLaneCeilingReadErrorsRefuse: every failed read is an error, never
// an uncapped ceiling; a good row admits.
func TestLaneCeilingReadErrorsRefuse(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	good := SeedCeilingLane(t, s, "good", BillingTierFree, RuntimeAPI, "api.example.com", knownPolicy)
	if err := s.UpsertCredential(ctx, Credential{ID: "orphan", AccountRef: "acct-good", QuotaDomainRef: "good:api",
		SecretRef: "provider.orphan.key", RuntimeProfileRef: "prof-good", Health: CredentialOK}); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}
	c, err := s.CeilingForRegistryLane(ctx, "good", LaneKindExternalAPI, false)
	if err != nil || !TierAdmits(provider.SensitivityPublic, c) {
		t.Fatalf("good row = %+v, %v; want public admitted", c, err)
	}
	checks := map[string]func() error{
		"unknown lane":     func() error { _, err := s.CeilingForLane(ctx, "nope", false); return err },
		"unknown registry": func() error { _, err := s.CeilingForRegistryLane(ctx, "nope", LaneKindExternalAPI, false); return err },
		"no lane row": func() error {
			_, err := s.CeilingForRegistryLane(ctx, "orphan", LaneKindExternalAPI, false)
			return err
		},
		"unresolved":    func() error { _, err := s.CeilingForRegistryLane(ctx, "good", LaneKindUnresolved, true); return err },
		"bad lane type": func() error { _, err := s.CeilingForRegistryLane(ctx, "good", "satellite", true); return err },
		"bad tier": func() error {
			_, err := computeCeiling(LaneKindExternalAPI, true, QuotaDomain{BillingTier: "gold"}, true, ProviderPolicy{})
			return err
		},
		"nil table": func() error {
			prev := SensitivityTable
			SensitivityTable = nil
			defer func() { SensitivityTable = prev }()
			_, err := s.CeilingForLane(ctx, good, false)
			return err
		},
		"empty table": func() error {
			prev := SensitivityTable
			SensitivityTable = func(provider.SensitivityTier, LaneKind, bool) bool { return false }
			defer func() { SensitivityTable = prev }()
			_, err := s.CeilingForLane(ctx, good, false)
			return err
		},
	}
	for name, fn := range checks {
		if fn() == nil {
			t.Errorf("%s: read returned no error", name)
		}
	}
	assertClosedDBRefuses(t, s, good)
}

// assertClosedDBRefuses: after Close both ceiling reads return an error.
func assertClosedDBRefuses(t *testing.T, s *Store, good LaneID) {
	t.Helper()
	if err := s.rdb.Close(); err != nil {
		t.Fatal(err)
	} else if _, err := s.CeilingForLane(context.Background(), good, false); err == nil {
		t.Error("closed DB: CeilingForLane returned no error")
	} else if _, err := s.CeilingForRegistryLane(context.Background(), "good", LaneKindExternalAPI, false); err == nil {
		t.Error("closed DB: CeilingForRegistryLane returned no error")
	}
}

// TestCeilingComputedForEveryLane: every live lane has a ceiling and a
// billing change shows on the next read (no cache).
func TestCeilingComputedForEveryLane(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	SetPaidEvidenceForTest(t, func(context.Context, *Store, QuotaDomain) (bool, error) { return true, nil })
	SeedCeilingLane(t, s, "one", BillingTierPaid, RuntimeAPI, "api.example.com", knownPolicy)
	SeedCeilingLane(t, s, "two", BillingTierPaid, RuntimeOllama, "127.0.0.1", knownPolicy)
	lanes, err := s.ListLanes(ctx, false)
	if err != nil || len(lanes) != 2 {
		t.Fatalf("ListLanes = %d, %v", len(lanes), err)
	}
	for _, l := range lanes {
		c, err := s.CeilingForLane(ctx, l.ID, false)
		if err != nil || !c.Reason.Valid() || c.DomainID != l.QuotaDomainRef {
			t.Fatalf("lane %s ceiling = %+v, %v", l.ID, c, err)
		}
		setTier(t, s, l.QuotaDomainRef, BillingTierDiscover)
		if again, _ := s.CeilingForLane(ctx, l.ID, false); again.Tier != provider.SensitivityPublic {
			t.Fatalf("lane %s after demotion = %+v, want public", l.ID, again)
		}
	}
}
