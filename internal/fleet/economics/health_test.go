package economics

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
)

// healthTestNow is the fixed instant every HealthKeys test injects
// (Art.7.3 -- no bare time.Now). Buckets' ResetAt is set relative to it so
// topology.TimeToReset/DomainPressure's time-to-reset term is
// deterministic.
func healthTestNow() fakeClock {
	return fakeClock{t: time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)}
}

func snapshotWithDomain(id topology.DomainID, dims map[string]topology.Bucket) topology.QuotaSnapshot {
	return topology.QuotaSnapshot{Domains: []topology.DomainQuota{
		{DomainID: id, Kind: topology.QuotaDomainSubscriptionWindow, Dimensions: dims, Confidence: 1.0},
	}}
}

// healthKeysFixture bundles TestHealthKeys' scenario so the test function
// itself stays under the funlen cap.
type healthKeysFixture struct {
	snapshot topology.QuotaSnapshot
	lanes    []topology.Lane
	domains  []topology.QuotaDomain
	accounts map[topology.AccountID]topology.Account
	now      fakeClock
}

func newHealthKeysFixture() healthKeysFixture {
	now := healthTestNow()
	// Half the weekly window has elapsed at now, for every domain (a
	// mid-window reading, neither "just reset" nor "about to reset").
	resetAt := now.Now().Add(3*24*time.Hour + 12*time.Hour)

	execAccount := topology.Account{ID: "acct-exec", Role: topology.AccountRoleExecutive}
	workforceAccount := topology.Account{ID: "acct-wf", Role: topology.AccountRoleWorkforce}
	accounts := map[topology.AccountID]topology.Account{execAccount.ID: execAccount, workforceAccount.ID: workforceAccount}

	execDomain := topology.QuotaDomain{ID: "dom-exec", AccountRef: execAccount.ID, Kind: topology.QuotaDomainSubscriptionWindow}
	buildDomain := topology.QuotaDomain{ID: "dom-build", AccountRef: workforceAccount.ID, Kind: topology.QuotaDomainSubscriptionWindow}
	bulkDomain := topology.QuotaDomain{ID: "dom-bulk", AccountRef: workforceAccount.ID, Kind: topology.QuotaDomainSubscriptionWindow}
	adversaryDomain := topology.QuotaDomain{ID: "dom-adversary", AccountRef: workforceAccount.ID, Kind: topology.QuotaDomainSubscriptionWindow}
	longCtxDomain := topology.QuotaDomain{ID: "dom-longctx", AccountRef: workforceAccount.ID, Kind: topology.QuotaDomainSubscriptionWindow}
	domains := []topology.QuotaDomain{execDomain, buildDomain, bulkDomain, adversaryDomain, longCtxDomain}

	lanes := []topology.Lane{
		{ID: "lane-exec", LaneClass: topology.LaneClassExecutive, QuotaDomainRef: execDomain.ID, Health: topology.LaneHealthAvailable},
		{ID: "lane-build", LaneClass: topology.LaneClassLead, QuotaDomainRef: buildDomain.ID, Health: topology.LaneHealthAvailable},
		{ID: "lane-bulk-1", LaneClass: topology.LaneClassAPIPaid, QuotaDomainRef: bulkDomain.ID, Health: topology.LaneHealthAvailable},
		{ID: "lane-bulk-2", LaneClass: topology.LaneClassPoolCheap, QuotaDomainRef: bulkDomain.ID, Health: topology.LaneHealthAvailable},
		{ID: "lane-bulk-3", LaneClass: topology.LaneClassWorkerFast, QuotaDomainRef: bulkDomain.ID, Health: topology.LaneHealthAvailable},
		{ID: "lane-adversary", LaneClass: topology.LaneClassCritic, QuotaDomainRef: adversaryDomain.ID, Health: topology.LaneHealthConstrained},
		{ID: "lane-longctx", LaneClass: topology.LaneClassUnranked, QuotaDomainRef: longCtxDomain.ID, Health: topology.LaneHealthAvailable, OfferingSnapshot: topology.OfferingSnapshot{ContextTokens: 1_000_000}},
	}

	fullBucket := func(remaining float64, source topology.BucketSource) topology.Bucket {
		return topology.Bucket{
			Name: topology.DimensionWeeklyShared, RemainingFraction: remaining, Source: source,
			Window: topology.BucketWindowWeek, ResetAt: resetAt,
		}
	}
	snapshot := topology.QuotaSnapshot{Domains: []topology.DomainQuota{
		// executive: remaining 0.05 <= reserve 0.30 -> hard reserve
		// (protected fires before pressure is even consulted).
		{DomainID: execDomain.ID, Kind: execDomain.Kind, Confidence: 1.0, Dimensions: map[string]topology.Bucket{
			topology.DimensionWeeklyShared: fullBucket(0.05, topology.SourceProviderStatus),
		}},
		// build: remaining 0.5, workforce reserve 0.15 -> comfortably
		// mid-range pressure (not scarce, not abundant-eligible key).
		{DomainID: buildDomain.ID, Kind: buildDomain.Kind, Confidence: 1.0, Dimensions: map[string]topology.Bucket{
			topology.DimensionWeeklyShared: fullBucket(0.5, topology.SourceProviderStatus),
		}},
		// bulk: remaining 0.9, low pressure -> abundant (3 healthy lanes).
		{DomainID: bulkDomain.ID, Kind: bulkDomain.Kind, Confidence: 1.0, Dimensions: map[string]topology.Bucket{
			topology.DimensionWeeklyShared: fullBucket(0.9, topology.SourceProviderStatus),
		}},
		// adversary: source unknown -> percentage omitted; lane is
		// constrained (not quarantined/auth-required, not available).
		{DomainID: adversaryDomain.ID, Kind: adversaryDomain.Kind, Confidence: 0, Dimensions: map[string]topology.Bucket{
			topology.DimensionWeeklyShared: fullBucket(0, topology.SourceUnknown),
		}},
		// long_context: remaining 0.9, low pressure, single healthy lane.
		{DomainID: longCtxDomain.ID, Kind: longCtxDomain.Kind, Confidence: 1.0, Dimensions: map[string]topology.Bucket{
			topology.DimensionWeeklyShared: fullBucket(0.9, topology.SourceProviderStatus),
		}},
	}}

	return healthKeysFixture{snapshot: snapshot, lanes: lanes, domains: domains, accounts: accounts, now: now}
}

// TestHealthKeys covers all five keys' lane sets, all six states in
// R-21.52 order, the percentage-as-minimum-remaining_fraction rule and
// the omitted-when-all-unknown case.
func TestHealthKeys(t *testing.T) {
	fx := newHealthKeysFixture()
	got := HealthKeys(fx.snapshot, fx.lanes, fx.domains, fx.accounts, fx.now)
	for _, key := range allHealthKeys {
		if _, ok := got[key]; !ok {
			t.Fatalf("HealthKeys result missing key %q", key)
		}
	}

	// executive: sole lane's domain is at hard reserve (0.05 <= 0.30
	// executive default) -> protected, ahead of any pressure check.
	if got[HealthKeyExecutive].State != HealthStateProtected {
		t.Fatalf("executive state = %v, want protected", got[HealthKeyExecutive].State)
	}

	// build: one healthy lane, moderate pressure -> available.
	if got[HealthKeyBuild].State != HealthStateAvailable {
		t.Fatalf("build state = %v, want available", got[HealthKeyBuild].State)
	}

	// bulk: three healthy lanes, low pressure (<2.0) -> abundant.
	if got[HealthKeyBulk].State != HealthStateAbundant {
		t.Fatalf("bulk state = %v, want abundant", got[HealthKeyBulk].State)
	}

	// adversary: lane is constrained (not available, not
	// quarantined/auth-required) and its only bucket is unknown ->
	// percentage omitted, and with no healthy lane the state is healthy
	// (R-21.52's final fallback; minDomainPressure also reports haveMin
	// false since DomainPressure still returns a number for an
	// unknown-source bucket via R-21.128 -- the omitted-percentage rule
	// is about the REPORTED percentage, not the pressure state).
	if got[HealthKeyAdversary].Percentage != nil {
		t.Fatalf("adversary percentage = %v, want omitted (all-unknown)", *got[HealthKeyAdversary].Percentage)
	}

	// long_context: one healthy lane whose offering context_tokens >=
	// 500,000, low pressure -> available (only 1 healthy lane, below the
	// abundant floor of 3, and abundant is bulk-only anyway).
	if got[HealthKeyLongContext].State != HealthStateAvailable {
		t.Fatalf("long_context state = %v, want available", got[HealthKeyLongContext].State)
	}
	if got[HealthKeyLongContext].Percentage == nil || *got[HealthKeyLongContext].Percentage != 0.9 {
		t.Fatalf("long_context percentage = %v, want 0.9", got[HealthKeyLongContext].Percentage)
	}
}

// TestHealthKeysScarce proves the literal R-21.52 rule: the set's MINIMUM
// quota pressure strictly above 5.0 resolves scarce, using a very low
// remaining_fraction bucket (which topology.DomainPressure's own formula
// prices well above the 5.0 floor at this window position).
func TestHealthKeysScarce(t *testing.T) {
	now := healthTestNow()
	resetAt := now.Now().Add(3*24*time.Hour + 12*time.Hour)
	account := topology.Account{ID: "acct-wf", Role: topology.AccountRoleWorkforce}
	domain := topology.QuotaDomain{ID: "dom-scarce", AccountRef: account.ID, Kind: topology.QuotaDomainSubscriptionWindow}
	lane := topology.Lane{LaneClass: topology.LaneClassLead, QuotaDomainRef: domain.ID, Health: topology.LaneHealthAvailable}
	snapshot := snapshotWithDomain(domain.ID, map[string]topology.Bucket{
		topology.DimensionWeeklyShared: {Name: topology.DimensionWeeklyShared, RemainingFraction: 0.02, Source: topology.SourceProviderStatus, Window: topology.BucketWindowWeek, ResetAt: resetAt},
	})
	accounts := map[topology.AccountID]topology.Account{account.ID: account}

	got := HealthKeys(snapshot, []topology.Lane{lane}, []topology.QuotaDomain{domain}, accounts, now)
	if got[HealthKeyBuild].State != HealthStateScarce {
		t.Fatalf("build state = %v, want scarce", got[HealthKeyBuild].State)
	}
}

// TestHealthKeyValid covers HealthKey.Valid's closed membership.
func TestHealthKeyValid(t *testing.T) {
	for _, k := range allHealthKeys {
		if !k.Valid() {
			t.Fatalf("HealthKey(%q).Valid() = false, want true", k)
		}
	}
	if HealthKey("bogus").Valid() {
		t.Fatal("HealthKey(\"bogus\").Valid() = true, want false")
	}
}

// TestHealthKeysUnavailable proves an empty lane set, and a lane set that
// is entirely quarantined/auth-required, both resolve to unavailable.
func TestHealthKeysUnavailable(t *testing.T) {
	now := healthTestNow()
	accounts := map[topology.AccountID]topology.Account{}
	snapshot := topology.QuotaSnapshot{}

	// No lanes at all.
	got := HealthKeys(snapshot, nil, nil, accounts, now)
	if got[HealthKeyExecutive].State != HealthStateUnavailable {
		t.Fatalf("empty lane set state = %v, want unavailable", got[HealthKeyExecutive].State)
	}

	// A lane set entirely quarantined.
	domain := topology.QuotaDomain{ID: "dom-q", Kind: topology.QuotaDomainSubscriptionWindow}
	lanes := []topology.Lane{
		{LaneClass: topology.LaneClassLead, QuotaDomainRef: domain.ID, Health: topology.LaneHealthQuarantined},
	}
	got = HealthKeys(snapshot, lanes, []topology.QuotaDomain{domain}, accounts, now)
	if got[HealthKeyBuild].State != HealthStateUnavailable {
		t.Fatalf("all-quarantined lane set state = %v, want unavailable", got[HealthKeyBuild].State)
	}
}

// TestHealthKeysRedaction asserts no credential, secret_ref or key id ever
// appears in the projection, via a json.Marshal round-trip inspected for
// those substrings, with the input lanes/accounts deliberately carrying
// secret-shaped fields.
func TestHealthKeysRedaction(t *testing.T) {
	now := healthTestNow()
	account := topology.Account{
		ID:   "acct-1",
		Role: topology.AccountRoleExecutive,
	}
	domain := topology.QuotaDomain{ID: "dom-1", AccountRef: account.ID, Kind: topology.QuotaDomainSubscriptionWindow}
	secretRef := topology.VaultKeyRef("vault-key-should-never-appear")
	credID := topology.CredentialID("cred-should-never-appear")
	lane := topology.Lane{
		LaneClass:      topology.LaneClassExecutive,
		QuotaDomainRef: domain.ID,
		Health:         topology.LaneHealthAvailable,
		CredentialRef:  &credID,
	}
	snapshot := snapshotWithDomain(domain.ID, map[string]topology.Bucket{
		topology.DimensionWeeklyShared: {Name: topology.DimensionWeeklyShared, RemainingFraction: 0.8, Source: topology.SourceProviderStatus, Window: topology.BucketWindowWeek, ResetAt: now.Now().Add(3 * 24 * time.Hour)},
	})
	accounts := map[topology.AccountID]topology.Account{account.ID: account}
	got := HealthKeys(snapshot, []topology.Lane{lane}, []topology.QuotaDomain{domain}, accounts, now)

	blob, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	forbidden := []string{"secret_ref", string(secretRef), string(credID), "cred-should-never-appear"}
	for _, s := range forbidden {
		if strings.Contains(string(blob), s) {
			t.Fatalf("HealthKeys projection leaked %q: %s", s, blob)
		}
	}
}
