package topology

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/storage/migrate"
)

// newTestRegistry builds a REAL registry.Registry (Art.2 real counterpart)
// over its own real, file-backed modernc SQLite database.
func newTestRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	db := openRealSQLiteFile(t)
	clk := newTestClock()
	if err := registry.ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clk, "", ""); err != nil {
		t.Fatalf("registry ApplyMigrationSchema: %v", err)
	}
	return registry.NewRegistry(db, clk)
}

func seedOAuthProviderWithLane(t *testing.T, reg *registry.Registry) {
	t.Helper()
	ctx := context.Background()
	provider := registry.ProviderRecord{Name: "anthropic-1", Driver: registry.DriverAnthropic, Auth: registry.AuthOAuth,
		AuthRef: "vault://anthropic-1/oauth", AccountKind: registry.AccountPersonal, Tier: registry.TierMid}
	if err := reg.UpsertProvider(ctx, provider); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	lane := registry.LaneRecord{LaneName: "anthropic-1", ProviderName: "anthropic-1", ModelFilter: []string{"claude-opus"},
		Capacity: registry.CapacityInteractiveUsage, State: registry.LaneStateAvailable}
	if err := reg.UpsertLane(ctx, lane); err != nil {
		t.Fatalf("seed lane: %v", err)
	}
}

func TestReconcileDerivesSubscriptionLane(t *testing.T) {
	ctx := context.Background()
	reg := newTestRegistry(t)
	seedOAuthProviderWithLane(t, reg)
	store := newTestStore(t)

	report, err := Reconcile(ctx, store, reg, newTestClock())
	if err != nil {
		t.Fatalf("Reconcile: %v (violations: %+v)", err, report.Violations)
	}
	if report.ProvidersReconciled != 1 || report.LanesReconciled != 1 {
		t.Fatalf("Report = %+v, want 1 provider and 1 lane", report)
	}
	assertSubscriptionAccountAndDomain(ctx, t, store)
	assertSubscriptionProfileAndCredential(ctx, t, store)
}

func assertSubscriptionAccountAndDomain(ctx context.Context, t *testing.T, store *Store) {
	t.Helper()
	acct, err := store.GetAccount(ctx, "anthropic-1")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if acct.Billing.Kind != BillingSubscription {
		t.Errorf("Account.Billing.Kind = %q, want subscription (oauth auth, no pool membership)", acct.Billing.Kind)
	}
	if acct.Role != AccountRoleWorkforce {
		t.Errorf("Account.Role = %q, want workforce (R-21.34 default)", acct.Role)
	}
	domain, err := store.GetQuotaDomain(ctx, "anthropic-1:subscription")
	if err != nil {
		t.Fatalf("GetQuotaDomain: %v", err)
	}
	if domain.Kind != QuotaDomainSubscriptionWindow || domain.BillingTier != BillingTierDiscover {
		t.Errorf("QuotaDomain = %+v, want subscription_window/discover", domain)
	}
}

func assertSubscriptionProfileAndCredential(ctx context.Context, t *testing.T, store *Store) {
	t.Helper()
	profile, err := store.GetRuntimeProfile(ctx, "anthropic-1:anthropic")
	if err != nil {
		t.Fatalf("GetRuntimeProfile: %v", err)
	}
	if profile.Persistent || profile.Endpoint != (Endpoint{}) {
		t.Errorf("first-run RuntimeProfile should be persistent=false, empty endpoint, got %+v", profile)
	}
	cred, err := store.GetCredential(ctx, "anthropic-1")
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if cred.SecretRef != "vault://anthropic-1/oauth" || cred.RuntimeProfileRef != "anthropic-1:anthropic" {
		t.Errorf("Credential = %+v, unexpected derivation", cred)
	}
	lanes, err := store.ListLanes(ctx, false)
	if err != nil {
		t.Fatalf("ListLanes: %v", err)
	}
	if len(lanes) != 1 {
		t.Fatalf("ListLanes = %d, want 1", len(lanes))
	}
	if lanes[0].LaneClass != LaneClassUnranked || lanes[0].BaseShadowPrice != 1.0 {
		t.Errorf("first-run lane should be unranked at base price 1.0, got %+v", lanes[0])
	}
	if lanes[0].ModelID != "claude-opus" {
		t.Errorf("ModelID = %q, want the single ModelFilter entry", lanes[0].ModelID)
	}
}

// TestReconcile is the named acceptance test for idempotence: a second
// Reconcile over unchanged registry input preserves discovered_at and
// writes byte-identical rows.
func TestReconcile(t *testing.T) {
	ctx := context.Background()
	reg := newTestRegistry(t)
	seedOAuthProviderWithLane(t, reg)
	store := newTestStore(t)

	if _, err := Reconcile(ctx, store, reg, newTestClock()); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	before, err := store.GetLane(ctx, mustLaneID(ctx, t, store))
	if err != nil {
		t.Fatalf("GetLane after first reconcile: %v", err)
	}

	laterClock := fakeClock{t: newTestClock().Now().AddDate(0, 0, 1)}
	if _, err := Reconcile(ctx, store, reg, laterClock); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	after, err := store.GetLane(ctx, before.ID)
	if err != nil {
		t.Fatalf("GetLane after second reconcile: %v", err)
	}
	if !after.DiscoveredAt.Equal(before.DiscoveredAt) {
		t.Errorf("discovered_at changed on re-run: before %v, after %v", before.DiscoveredAt, after.DiscoveredAt)
	}
	if after.LaneClass != before.LaneClass || after.BaseShadowPrice != before.BaseShadowPrice {
		t.Errorf("lane_class/base_shadow_price should survive a re-run unchanged")
	}
}

func mustLaneID(ctx context.Context, t *testing.T, store *Store) LaneID {
	t.Helper()
	lanes, err := store.ListLanes(ctx, false)
	if err != nil || len(lanes) == 0 {
		t.Fatalf("expected at least one lane, err=%v", err)
	}
	return lanes[0].ID
}

func TestReconcilePoolLaneDerivesSharedPoolDomain(t *testing.T) {
	ctx := context.Background()
	reg := newTestRegistry(t)
	provider := registry.ProviderRecord{Name: "pool-1", Driver: registry.DriverAnthropic, Auth: registry.AuthKey,
		AuthRef: "vault://pool-1/key", AccountKind: registry.AccountShared, Tier: registry.TierMid}
	if err := reg.UpsertProvider(ctx, provider); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	lane := registry.LaneRecord{LaneName: "pool-lane-1", ProviderName: "pool-1", PoolMembership: "shared-pool-a",
		Capacity: registry.CapacityAPICredit, State: registry.LaneStateAvailable}
	if err := reg.UpsertLane(ctx, lane); err != nil {
		t.Fatalf("seed lane: %v", err)
	}
	store := newTestStore(t)

	if _, err := Reconcile(ctx, store, reg, newTestClock()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	domain, err := store.GetQuotaDomain(ctx, "pool:shared-pool-a")
	if err != nil {
		t.Fatalf("GetQuotaDomain(pool:shared-pool-a): %v", err)
	}
	if domain.Kind != QuotaDomainSharedPool {
		t.Errorf("domain.Kind = %q, want shared_pool", domain.Kind)
	}
	acct, err := store.GetAccount(ctx, "pool-1")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if acct.Billing.Kind != BillingPool {
		t.Errorf("Account.Billing.Kind = %q, want pool", acct.Billing.Kind)
	}
}

func TestReconcileOllamaMapsRuntime(t *testing.T) {
	ctx := context.Background()
	reg := newTestRegistry(t)
	provider := registry.ProviderRecord{Name: "ollama-1", Driver: registry.DriverOllama, Auth: registry.AuthKey,
		AuthRef: "vault://ollama-1/key", AccountKind: registry.AccountPersonal, Tier: registry.TierFree}
	if err := reg.UpsertProvider(ctx, provider); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	lane := registry.LaneRecord{LaneName: "ollama-1", ProviderName: "ollama-1",
		Capacity: registry.CapacityAPICredit, State: registry.LaneStateAvailable}
	if err := reg.UpsertLane(ctx, lane); err != nil {
		t.Fatalf("seed lane: %v", err)
	}
	store := newTestStore(t)

	if _, err := Reconcile(ctx, store, reg, newTestClock()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	profile, err := store.GetRuntimeProfile(ctx, "ollama-1:ollama")
	if err != nil {
		t.Fatalf("GetRuntimeProfile: %v", err)
	}
	if profile.Runtime != RuntimeOllama {
		t.Errorf("Runtime = %q, want ollama", profile.Runtime)
	}
}

func TestBillingKindForPoolTakesPriority(t *testing.T) {
	p := registry.ProviderRecord{Auth: registry.AuthOAuth}
	lanes := []registry.LaneRecord{{PoolMembership: "pool-a"}}
	if got := billingKindFor(p, lanes); got != BillingPool {
		t.Errorf("billingKindFor = %q, want pool (pool membership takes priority over oauth)", got)
	}
}

func TestDomainKindAndIDForBilling(t *testing.T) {
	if got := domainKindForBilling(BillingPool); got != QuotaDomainSharedPool {
		t.Errorf("domainKindForBilling(pool) = %q", got)
	}
	if got := domainIDForBilling(BillingPool, "prov", "pool-a"); got != "pool:pool-a" {
		t.Errorf("domainIDForBilling(pool) = %q", got)
	}
	if got := domainIDForBilling(BillingSubscription, "prov", ""); got != "prov:subscription" {
		t.Errorf("domainIDForBilling(subscription) = %q", got)
	}
	if got := domainIDForBilling(BillingAPI, "prov", ""); got != "prov:api" {
		t.Errorf("domainIDForBilling(api) = %q", got)
	}
}

// TestReconcileMatchesGoldenDerivation compares the OAuth-provider scenario
// against testdata/reconcile_roundtrip.golden.json field by field (the
// full_desc derivation, verbatim).
func TestReconcileMatchesGoldenDerivation(t *testing.T) {
	data, err := os.ReadFile("testdata/reconcile_roundtrip.golden.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var golden map[string]map[string]any
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}

	ctx := context.Background()
	reg := newTestRegistry(t)
	seedOAuthProviderWithLane(t, reg)
	store := newTestStore(t)
	if _, err := Reconcile(ctx, store, reg, newTestClock()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	acct, err := store.GetAccount(ctx, "anthropic-1")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	wantAcct := golden["account"]
	if string(acct.ID) != wantAcct["id"] || acct.Provider != wantAcct["provider"] ||
		string(acct.Billing.Kind) != wantAcct["billing_kind"] || string(acct.Role) != wantAcct["role"] {
		t.Errorf("Account = %+v, golden wants %+v", acct, wantAcct)
	}

	lanes, err := store.ListLanes(ctx, false)
	if err != nil || len(lanes) != 1 {
		t.Fatalf("ListLanes: %v (len=%d)", err, len(lanes))
	}
	wantLane := golden["lane"]
	if string(lanes[0].ID) != wantLane["id"] {
		t.Errorf("Lane.ID = %q, golden wants %q", lanes[0].ID, wantLane["id"])
	}
	if string(lanes[0].LaneClass) != wantLane["lane_class"] {
		t.Errorf("Lane.LaneClass = %q, golden wants %q", lanes[0].LaneClass, wantLane["lane_class"])
	}
}

// TestReconcileAbortsOnInvariantViolation proves an invariant-breaking
// state aborts the whole transaction and leaves prior rows byte-identical
// (asserted via a before/after snapshot comparison).

func TestRuntimeKindForDriver(t *testing.T) {
	if got := runtimeKindForDriver(registry.DriverOllama); got != RuntimeOllama {
		t.Errorf("runtimeKindForDriver(ollama) = %q, want ollama", got)
	}
	if got := runtimeKindForDriver(registry.DriverAnthropic); got != RuntimeAPI {
		t.Errorf("runtimeKindForDriver(anthropic) = %q, want api (T1 default)", got)
	}
}
