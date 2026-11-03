package topology

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
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
	seedRegistry(context.Background(), t, reg, registry.ProviderRecord{Name: "anthropic-1", Driver: registry.DriverAnthropic,
		Auth: registry.AuthOAuth, AuthRef: "vault://anthropic-1/oauth", AccountKind: registry.AccountPersonal, Tier: registry.TierMid},
		registry.LaneRecord{LaneName: "anthropic-1", ProviderName: "anthropic-1", ModelFilter: []string{"claude-opus"},
			Capacity: registry.CapacityInteractiveUsage, State: registry.LaneStateAvailable})
}

func seedRegistry(ctx context.Context, t *testing.T, reg *registry.Registry, p registry.ProviderRecord, l registry.LaneRecord) {
	t.Helper()
	if err := reg.UpsertProvider(ctx, p); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	if err := reg.UpsertLane(ctx, l); err != nil {
		t.Fatalf("seed lane: %v", err)
	}
}

func TestReconcileDerivesSubscriptionLane(t *testing.T) {
	ctx, reg, store := context.Background(), newTestRegistry(t), newTestStore(t)
	seedOAuthProviderWithLane(t, reg)
	if report := mustPass(ctx, t, store, reg, newTestClock()); report.ProvidersReconciled != 1 || report.LanesReconciled != 1 {
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
	if err != nil || len(lanes) != 1 {
		t.Fatalf("ListLanes = %d lanes (%v), want 1", len(lanes), err)
	}
	if lanes[0].LaneClass != LaneClassUnranked || lanes[0].BaseShadowPrice != 1.0 {
		t.Errorf("first-run lane should be unranked at base price 1.0, got %+v", lanes[0])
	}
	if lanes[0].ModelID != "claude-opus" {
		t.Errorf("ModelID = %q, want the single ModelFilter entry", lanes[0].ModelID)
	}
}

// TestReconcile: a re-run over unchanged input keeps every lane row, discovered_at included.
func TestReconcile(t *testing.T) {
	ctx, reg, store := context.Background(), newTestRegistry(t), newTestStore(t)
	seedOAuthProviderWithLane(t, reg)
	mustPass(ctx, t, store, reg, newTestClock())
	before := mustSnapshot(ctx, t, store).Lanes
	mustPass(ctx, t, store, reg, fakeClock{t: newTestClock().Now().AddDate(0, 0, 1)})
	if after := mustSnapshot(ctx, t, store).Lanes; len(before) != 1 || !reflect.DeepEqual(after, before) {
		t.Errorf("lane rows changed on re-run:\nbefore %+v\nafter  %+v", before, after)
	}
}

// TestReconcileIdempotent: a second pass over unchanged inputs changes no
// row of any table and reports zero counts; a failure injected after the
// credential write (a test-only trigger on the lane table) leaves the
// pre-pass rows, and the next pass converges.
func TestReconcileIdempotent(t *testing.T) {
	ctx, reg, store := context.Background(), newTestRegistry(t), newTestStore(t)
	seedOAuthProviderWithLane(t, reg)
	at := func(days int) fakeClock { return fakeClock{t: newTestClock().Now().AddDate(0, 0, days)} }
	if rep := mustPass(ctx, t, store, reg, at(0)); rep.RowsWritten != 5 || rep.LanesReconciled != 1 {
		t.Fatalf("first pass Report = %+v, want 5 rows and 1 lane written", rep)
	}
	assertZeroPass(ctx, t, store, reg, at(1))
	before := mustSnapshot(ctx, t, store)
	mustExec(ctx, t, store, `CREATE TRIGGER inject_fail BEFORE INSERT ON `+tableLane+` BEGIN SELECT RAISE(ABORT, 'injected lane failure'); END`)
	p, err := reg.GetProvider(ctx, "anthropic-1")
	if p.AuthRef = "vault://anthropic-1/rotated"; err != nil || reg.UpsertProvider(ctx, p) != nil {
		t.Fatalf("rotate the provider auth ref: %v", err)
	}
	rep, err := Reconcile(ctx, store, reg, at(2))
	if err == nil || !strings.Contains(err.Error(), "injected lane failure") || rep.RowsWritten != 0 {
		t.Fatalf("injected failure: Report = %+v, err = %v", rep, err)
	}
	if got := mustSnapshot(ctx, t, store); !reflect.DeepEqual(got, before) {
		t.Fatalf("the failed pass changed rows:\nbefore %+v\nafter  %+v", before, got)
	}
	mustExec(ctx, t, store, `DROP TRIGGER inject_fail`)
	if rep := mustPass(ctx, t, store, reg, at(3)); rep.RowsWritten != 1 {
		t.Fatalf("converging pass Report = %+v, want only the credential written", rep)
	}
	if c, err := store.GetCredential(ctx, "anthropic-1"); err != nil || c.SecretRef != "vault://anthropic-1/rotated" {
		t.Fatalf("credential after converging pass = %+v (%v)", c, err)
	}
	assertZeroPass(ctx, t, store, reg, at(4))
}

func assertZeroPass(ctx context.Context, t *testing.T, store *Store, reg *registry.Registry, clock fakeClock) {
	t.Helper()
	before := mustSnapshot(ctx, t, store)
	if rep := mustPass(ctx, t, store, reg, clock); !reflect.DeepEqual(rep, Report{}) {
		t.Errorf("unchanged-input pass Report = %+v, want all zero", rep)
	}
	if after := mustSnapshot(ctx, t, store); !reflect.DeepEqual(after, before) {
		t.Errorf("unchanged-input pass changed rows:\nbefore %+v\nafter  %+v", before, after)
	}
}

func mustPass(ctx context.Context, t *testing.T, store *Store, reg *registry.Registry, clock fakeClock) Report {
	t.Helper()
	rep, err := Reconcile(ctx, store, reg, clock)
	if err != nil {
		t.Fatalf("Reconcile: %v (violations %+v)", err, rep.Violations)
	}
	return rep
}

func mustSnapshot(ctx context.Context, t *testing.T, store *Store) Snapshot {
	t.Helper()
	snap, err := store.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func mustExec(ctx context.Context, t *testing.T, store *Store, q string) {
	t.Helper()
	if _, err := store.rdb.ExecContext(ctx, q); err != nil {
		t.Fatal(err)
	}
}

func TestReconcilePoolLaneDerivesSharedPoolDomain(t *testing.T) {
	ctx, reg, store := context.Background(), newTestRegistry(t), newTestStore(t)
	seedRegistry(ctx, t, reg, registry.ProviderRecord{Name: "pool-1", Driver: registry.DriverAnthropic, Auth: registry.AuthKey,
		AuthRef: "vault://pool-1/key", AccountKind: registry.AccountShared, Tier: registry.TierMid},
		registry.LaneRecord{LaneName: "pool-lane-1", ProviderName: "pool-1", PoolMembership: "shared-pool-a",
			Capacity: registry.CapacityAPICredit, State: registry.LaneStateAvailable})
	mustPass(ctx, t, store, reg, newTestClock())
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
	ctx, reg, store := context.Background(), newTestRegistry(t), newTestStore(t)
	seedRegistry(ctx, t, reg, registry.ProviderRecord{Name: "ollama-1", Driver: registry.DriverOllama, Auth: registry.AuthKey,
		AuthRef: "vault://ollama-1/key", AccountKind: registry.AccountPersonal, Tier: registry.TierFree},
		registry.LaneRecord{LaneName: "ollama-1", ProviderName: "ollama-1",
			Capacity: registry.CapacityAPICredit, State: registry.LaneStateAvailable})
	mustPass(ctx, t, store, reg, newTestClock())
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

func TestRuntimeKindForDriver(t *testing.T) {
	if got := runtimeKindForDriver(registry.DriverOllama); got != RuntimeOllama {
		t.Errorf("runtimeKindForDriver(ollama) = %q, want ollama", got)
	}
	if got := runtimeKindForDriver(registry.DriverAnthropic); got != RuntimeAPI {
		t.Errorf("runtimeKindForDriver(anthropic) = %q, want api (T1 default)", got)
	}
}
