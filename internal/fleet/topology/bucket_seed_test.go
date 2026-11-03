package topology

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func seedTicketStore(t *testing.T) *Store {
	t.Helper()
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, home)
	}
	return newTestStore(t)
}

func TestSeedUndiscoveredBuckets(t *testing.T) {
	for _, kind := range []QuotaDomainKind{QuotaDomainAPIProject, QuotaDomainSubscriptionWindow, QuotaDomainSharedPool} {
		t.Run(string(kind), func(t *testing.T) { checkSeedDimensions(t, kind) })
	}
	t.Run("partial domain untouched", checkSeedExisting)
	t.Run("unknown kind refused", checkSeedUnknown)
	t.Run("read and write failures refused", checkSeedFailures)
}

func seedTicketDomain(t *testing.T, s *Store, kind QuotaDomainKind) QuotaDomain {
	t.Helper()
	ctx := context.Background()
	if err := s.UpsertAccount(ctx, Account{ID: "provider-x", Provider: "provider-x",
		Billing: BillingInfo{Kind: BillingAPI}, Role: AccountRoleWorkforce}); err != nil {
		t.Fatal(err)
	}
	d := QuotaDomain{ID: "domain-a", AccountRef: "provider-x", Kind: kind, BillingTier: BillingTierDiscover}
	if err := s.UpsertQuotaDomain(ctx, d); err != nil {
		t.Fatal(err)
	}
	return d
}

func checkSeedDimensions(t *testing.T, kind QuotaDomainKind) {
	ctx, s := context.Background(), seedTicketStore(t)
	d := seedTicketDomain(t, s, kind)
	q := NewQuotaStore(s.rdb)
	want := map[QuotaDomainKind][]string{
		QuotaDomainAPIProject:         {"rpm", "tpm", "rpd"},
		QuotaDomainSubscriptionWindow: {"session_5h", "weekly_shared", "weekly_model_fraction", "monthly"},
		QuotaDomainSharedPool:         {"window_5h", "weekly", "monthly"},
	}[kind]
	if n, err := SeedUndiscoveredBuckets(ctx, q, s); err != nil || n != len(want) {
		t.Fatalf("seed = %d, %v; want %d", n, err, len(want))
	}
	account, err := s.GetAccount(ctx, d.AccountRef)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range want {
		b, err := q.GetBucket(ctx, d.ID, name)
		if err != nil || b.Name != name || b.Limit != DiscoverLimit || b.CapacityObserved != UnobservedCapacity ||
			b.Source != SourceUnknown || b.LimitScopeID != ResolveScope(account, d) {
			t.Fatalf("bucket %s = %+v (%v)", name, b, err)
		}
	}
	before, err := q.ListBuckets(ctx, d.ID)
	if err != nil || len(before) != len(want) {
		t.Fatalf("buckets = %+v (%v)", before, err)
	}
	if n, err := SeedUndiscoveredBuckets(ctx, q, s); n != 0 || err != nil {
		t.Fatalf("second seed = %d, %v", n, err)
	}
	after, err := q.ListBuckets(ctx, d.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("second seed changed buckets: %+v (%v)", after, err)
	}
}

func checkSeedExisting(t *testing.T) {
	ctx, s := context.Background(), seedTicketStore(t)
	d := seedTicketDomain(t, s, QuotaDomainAPIProject)
	q := NewQuotaStore(s.rdb)
	b := undiscoveredBucket(DimensionRPM, d.Kind, AccountScope(d.AccountRef))
	b.Limit, b.CapacityObserved, b.Source = 17, 12, SourceUserEstimate
	if err := q.UpsertBucket(ctx, d.ID, DimensionRPM, b); err != nil {
		t.Fatal(err)
	}
	before, err := q.ListBuckets(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := SeedUndiscoveredBuckets(ctx, q, s); n != 0 || err != nil {
		t.Fatalf("seed existing = %d, %v", n, err)
	}
	after, err := q.ListBuckets(ctx, d.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("partial domain changed: %+v (%v)", after, err)
	}
}

func checkSeedUnknown(t *testing.T) {
	ctx, s := context.Background(), seedTicketStore(t)
	d := seedTicketDomain(t, s, QuotaDomainAPIProject)
	// Inject a future stored enum through a test-only seam.
	mustExec(ctx, t, s, `UPDATE config_quota_domain SET kind='future-kind'`)
	q := NewQuotaStore(s.rdb)
	n, err := SeedUndiscoveredBuckets(ctx, q, s)
	assertSeedInvariant(t, err, `quota_domain_kind "domain-a": unknown domain kind`)
	if n != 0 {
		t.Fatalf("unknown kind wrote %d buckets", n)
	}
	if buckets, err := q.ListBuckets(ctx, d.ID); err != nil || len(buckets) != 0 {
		t.Fatalf("unknown kind buckets = %+v (%v)", buckets, err)
	}
}

func assertSeedInvariant(t *testing.T, err error, message string) {
	t.Helper()
	if err == nil || errors.Unwrap(err) != ErrTopologyInvariant || !strings.Contains(err.Error(), message) ||
		!strings.Contains(err.Error(), ErrTopologyInvariant.Error()) {
		t.Fatalf("want invariant identity and %q, got %v", message, err)
	}
}

func checkSeedFailures(t *testing.T) {
	ctx, s := context.Background(), seedTicketStore(t)
	d := seedTicketDomain(t, s, QuotaDomainAPIProject)
	q := NewQuotaStore(s.rdb)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if n, err := SeedUndiscoveredBuckets(cancelled, q, s); n != 0 || err == nil {
		t.Fatalf("cancelled seed = %d, %v", n, err)
	}
	mustExec(ctx, t, s, `CREATE TRIGGER seed_fail BEFORE INSERT ON sessions_quota_bucket BEGIN SELECT RAISE(ABORT, 'injected seed failure'); END`)
	if n, err := SeedUndiscoveredBuckets(ctx, q, s); n != 0 || !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("failed write = %d, %v", n, err)
	}
	mustExec(ctx, t, s, `DROP TRIGGER seed_fail`)
	if n, err := SeedUndiscoveredBuckets(ctx, q, s); n != 3 || err != nil {
		t.Fatalf("recovered seed = %d, %v", n, err)
	}
	if err := s.rdb.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := q.ListBuckets(ctx, d.ID); err == nil {
		t.Fatal("closed database read succeeded")
	}
	if n, err := SeedUndiscoveredBuckets(ctx, q, s); n != 0 || err == nil {
		t.Fatalf("closed database seed = %d, %v", n, err)
	}
}
