package economics

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/providers/usage"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/provider"
)

// fakeClock is a fixed Clock for tests (Art.7.3 -- no bare time.Now). It
// satisfies economics.Clock, topology's migrate.Clock and usage.Clock
// structurally.
type fakeClock struct{ t time.Time }

func (c fakeClock) Now() time.Time { return c.t }

func newTestClock() fakeClock {
	return fakeClock{t: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}
}

// testHarness bundles the real, file-backed modernc SQLite stores this
// ticket accounts over: topology's Store/QuotaStore (AN/S-77.T3) and the
// usage domain's Manager/Reader (J/S-20.T4), each on its OWN database file
// under t.TempDir (Art.2.1/2.2 -- never an in-memory stand-in), exactly as
// the real composition root opens two separate database files (mirrors
// P1-E40-W9-S77-T3's daemon_unix_run.go precedent).
type testHarness struct {
	store  *topology.Store
	quotas *topology.QuotaStore
	usageR provider.UsageReader
	usageM *usage.Manager
	clock  fakeClock
}

func newTestHarness(t *testing.T) *testHarness {
	t.Helper()
	clock := newTestClock()

	topoPath := filepath.Join(t.TempDir(), "topology.db")
	topoDB, err := sql.Open("sqlite", "file:"+topoPath+"?_journal_mode=WAL&_foreign_keys=1")
	if err != nil {
		t.Fatalf("open topology db: %v", err)
	}
	topoDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = topoDB.Close() })
	if err := topology.ApplyMigrationSchema(context.Background(), topoDB, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("topology ApplyMigrationSchema: %v", err)
	}

	usagePath := filepath.Join(t.TempDir(), "provider-usage.db")
	usageDB, err := sql.Open("sqlite", "file:"+usagePath+"?_journal_mode=WAL&_foreign_keys=1")
	if err != nil {
		t.Fatalf("open usage db: %v", err)
	}
	usageDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = usageDB.Close() })
	if err := usage.ApplyMigrationSchema(context.Background(), usageDB, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("usage ApplyMigrationSchema: %v", err)
	}
	mgr := usage.NewManager(usageDB, clock)

	return &testHarness{
		store:  topology.NewStore(topoDB),
		quotas: topology.NewQuotaStore(topoDB),
		usageR: usage.NewReader(mgr),
		usageM: mgr,
		clock:  clock,
	}
}

func (h *testHarness) accountant() *WindowAccountant {
	return NewWindowAccountant(h.store, h.quotas, h.usageR, h.clock)
}

func seedAccountAndDomain(t *testing.T, h *testHarness, domainID topology.DomainID, providerName string) {
	t.Helper()
	ctx := context.Background()
	acct := topology.Account{ID: "acct-a", Provider: providerName, Billing: topology.BillingInfo{Kind: topology.BillingSubscription}, Role: topology.AccountRoleExecutive}
	if err := h.store.UpsertAccount(ctx, acct); err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}
	domain := topology.QuotaDomain{ID: domainID, AccountRef: acct.ID, Kind: topology.QuotaDomainSubscriptionWindow, BillingTier: topology.BillingTierPaid}
	if err := h.store.UpsertQuotaDomain(ctx, domain); err != nil {
		t.Fatalf("UpsertQuotaDomain: %v", err)
	}
}

func bucketNames(bs []topology.Bucket) map[string]topology.Bucket {
	out := make(map[string]topology.Bucket, len(bs))
	for _, b := range bs {
		out[b.Name] = b
	}
	return out
}

// TestWindowBucketsSourceLadder proves per-DIMENSION precedence: a
// provider-status bucket already stored for session_5h is returned
// unchanged, while monthly (no stored bucket, but usage rows exist)
// synthesises a user-estimate bucket from J/S-20.T4 QueryUsage rows.
func TestWindowBucketsSourceLadder(t *testing.T) {
	ctx := context.Background()
	h := newTestHarness(t)
	domainID := topology.DomainID("dom-1")
	seedAccountAndDomain(t, h, domainID, "anthropic")

	providerBucket := topology.Bucket{
		Name: topology.DimensionSession5h, Limit: 100, RemainingFraction: 0.8,
		ResetAt: h.clock.Now().Add(5 * time.Hour), Window: topology.BucketWindow5h,
		Source: topology.SourceProviderStatus, Confidence: 1.0, ObservedAt: h.clock.Now(),
		CapacityObserved: topology.UnobservedCapacity,
	}
	if err := h.quotas.UpsertBucket(ctx, domainID, providerBucket.Name, providerBucket); err != nil {
		t.Fatalf("UpsertBucket: %v", err)
	}
	// Usage activity within the monthly window, no stored monthly bucket.
	for i := 0; i < 5; i++ {
		if err := h.usageM.IncrementUsage(ctx, usage.IncrementRequest{ProviderName: "anthropic", TokensIn: 10}); err != nil {
			t.Fatalf("IncrementUsage: %v", err)
		}
	}

	got, err := h.accountant().Buckets(ctx, domainID)
	if err != nil {
		t.Fatalf("Buckets: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("expected exactly 4 dimensions, got %d", len(got))
	}
	byName := bucketNames(got)
	for _, dim := range subscriptionWindowDimensions {
		if _, ok := byName[dim]; !ok {
			t.Fatalf("missing dimension %q", dim)
		}
	}
	if s := byName[topology.DimensionSession5h].Source; s != topology.SourceProviderStatus {
		t.Fatalf("session_5h source = %q, want provider-status", s)
	}
	if s := byName[topology.DimensionMonthly].Source; s != topology.SourceUserEstimate {
		t.Fatalf("monthly source = %q, want user-estimate (from usage rows)", s)
	}
	if byName[topology.DimensionMonthly].Limit != topology.DiscoverLimit {
		t.Fatalf("monthly limit = %d, want DiscoverLimit", byName[topology.DimensionMonthly].Limit)
	}
}

// TestWindowBucketsUnknownNotInfinite proves a dimension with no provider
// bucket and no usage activity is still emitted, at source unknown, limit
// -1, confidence 0 -- never omitted, never treated as unlimited.
func TestWindowBucketsUnknownNotInfinite(t *testing.T) {
	ctx := context.Background()
	h := newTestHarness(t)
	domainID := topology.DomainID("dom-2")
	seedAccountAndDomain(t, h, domainID, "anthropic")

	got, err := h.accountant().Buckets(ctx, domainID)
	if err != nil {
		t.Fatalf("Buckets: %v", err)
	}
	byName := bucketNames(got)
	for _, dim := range subscriptionWindowDimensions {
		b, ok := byName[dim]
		if !ok {
			t.Fatalf("dimension %q was omitted, want emitted at source unknown", dim)
		}
		if b.Source != topology.SourceUnknown {
			t.Fatalf("dimension %q source = %q, want unknown", dim, b.Source)
		}
		if b.Limit != topology.DiscoverLimit {
			t.Fatalf("dimension %q limit = %d, want DiscoverLimit (-1), never treated as unlimited", dim, b.Limit)
		}
		if b.Confidence != 0 {
			t.Fatalf("dimension %q confidence = %v, want 0", dim, b.Confidence)
		}
	}
}

// TestWindowBucketsClockInjection proves reset_at for a synthesised bucket
// is derived from the INJECTED clock, not the wall clock: advancing the
// frozen clock changes reset_at by exactly the advance.
func TestWindowBucketsClockInjection(t *testing.T) {
	ctx := context.Background()
	h := newTestHarness(t)
	domainID := topology.DomainID("dom-3")
	seedAccountAndDomain(t, h, domainID, "anthropic")

	first, err := h.accountant().Buckets(ctx, domainID)
	if err != nil {
		t.Fatalf("Buckets: %v", err)
	}
	firstReset := bucketNames(first)[topology.DimensionSession5h].ResetAt

	advanced := fakeClock{t: h.clock.Now().Add(2 * time.Hour)}
	accountant2 := NewWindowAccountant(h.store, h.quotas, h.usageR, advanced)
	second, err := accountant2.Buckets(ctx, domainID)
	if err != nil {
		t.Fatalf("Buckets (advanced clock): %v", err)
	}
	secondReset := bucketNames(second)[topology.DimensionSession5h].ResetAt

	wantDelta := 2 * time.Hour
	gotDelta := secondReset.Sub(firstReset)
	if gotDelta != wantDelta {
		t.Fatalf("reset_at delta = %v, want %v (clock injection)", gotDelta, wantDelta)
	}
}

// TestWindowBucketsClosedStore proves the error path: a nil store field
// (Buckets called against a zero-value-adjacent accountant) fails closed
// with a typed error rather than panicking or silently returning zero
// buckets.
func TestWindowBucketsClosedStore(t *testing.T) {
	h := newTestHarness(t)
	accountant := NewWindowAccountant(nil, h.quotas, h.usageR, h.clock)
	if _, err := accountant.Buckets(context.Background(), "dom-x"); err == nil {
		t.Fatal("expected an error for a nil store")
	}
}

// TestWindowBucketsRejectsWrongDomainKind proves Buckets refuses a domain
// whose Kind is not subscription_window.
func TestWindowBucketsRejectsWrongDomainKind(t *testing.T) {
	ctx := context.Background()
	h := newTestHarness(t)
	acct := topology.Account{ID: "acct-b", Provider: "anthropic", Billing: topology.BillingInfo{Kind: topology.BillingAPI}, Role: topology.AccountRoleWorkforce}
	if err := h.store.UpsertAccount(ctx, acct); err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}
	domain := topology.QuotaDomain{ID: "dom-wrong-kind", AccountRef: acct.ID, Kind: topology.QuotaDomainAPIProject, BillingTier: topology.BillingTierPaid}
	if err := h.store.UpsertQuotaDomain(ctx, domain); err != nil {
		t.Fatalf("UpsertQuotaDomain: %v", err)
	}
	if _, err := h.accountant().Buckets(ctx, domain.ID); err == nil {
		t.Fatal("expected an error for a non-subscription_window domain")
	}
}

// windowBucketsGolden mirrors testdata/window_buckets.golden.json's shape.
type windowBucketsGolden struct {
	Dimensions []struct {
		Name   string `json:"name"`
		Window string `json:"window"`
	} `json:"dimensions"`
}

// TestWindowBucketsDimensionWindowGolden asserts the exact four dimension
// names and their window enum against a golden fixture hand-transcribed
// from R-21.26/R-21.116 (never captured from this package's own output),
// via topology.WindowFor -- the single frozen source both window.go and
// this golden agree with.
func TestWindowBucketsDimensionWindowGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/window_buckets.golden.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var golden windowBucketsGolden
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	if len(golden.Dimensions) != 4 {
		t.Fatalf("golden has %d dimensions, want 4", len(golden.Dimensions))
	}
	if len(subscriptionWindowDimensions) != len(golden.Dimensions) {
		t.Fatalf("subscriptionWindowDimensions has %d entries, golden has %d", len(subscriptionWindowDimensions), len(golden.Dimensions))
	}
	for i, want := range golden.Dimensions {
		if subscriptionWindowDimensions[i] != want.Name {
			t.Fatalf("dimension[%d] = %q, golden wants %q", i, subscriptionWindowDimensions[i], want.Name)
		}
		window, _, err := topology.WindowFor(want.Name)
		if err != nil {
			t.Fatalf("WindowFor(%q): %v", want.Name, err)
		}
		if string(window) != want.Window {
			t.Fatalf("WindowFor(%q) = %q, golden wants %q", want.Name, window, want.Window)
		}
	}
}
