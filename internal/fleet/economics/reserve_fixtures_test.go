package economics

// Purpose: shared reservation-suite test fixtures: a real, file-backed
//   modernc-sqlite database per t.TempDir (never an in-memory
//   self-authored double) migrated via ApplyReservationSchema, a base
//   ReserveRequest builder, a steppable injected clock, the sentinel
//   identity helper (errors.Is compares Kind only in this codebase), and
//   a real topology database whose buckets are written only through
//   topology.QuotaStore.UpsertBucket.
// SPORT: fleet/economics/reservation/ADD.

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver for this file's tests

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// openReservationTestDBAt opens a real modernc-sqlite database file at
// path and applies the reservation MigrationSet.
func openReservationTestDBAt(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := ApplyReservationSchema(context.Background(), db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyReservationSchema: %v", err)
	}
	return db
}

// openReservationTestDB opens a fresh migrated database under t.TempDir().
func openReservationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return openReservationTestDBAt(t, filepath.Join(t.TempDir(), "reservation-test.db"))
}

// newReservationTestStore opens a fresh test db and wraps it in a
// ReservationStore over the fixed test clock.
func newReservationTestStore(t *testing.T) *ReservationStore {
	t.Helper()
	store, err := NewReservationStore(openReservationTestDB(t), newTestClock())
	if err != nil {
		t.Fatalf("NewReservationStore: %v", err)
	}
	return store
}

var executionSeq atomic.Int64

// nextExecutionID returns a fresh execution id so two requests built
// from baseReserveRequest never collide on the unique execution index.
func nextExecutionID() string { return fmt.Sprintf("exec-%d", executionSeq.Add(1)) }

// baseReserveRequest returns a minimally-valid interactive ReserveRequest
// for tests to mutate. ScopeID is left empty: the Reserver derives it.
func baseReserveRequest() ReserveRequest {
	est, _ := EstimateFor(1000, conductor.TaskClassChat, 1)
	return ReserveRequest{
		ExecutionID: nextExecutionID(), JobID: "job-1", ProjectID: "project-1", LaneID: "lane-1",
		DomainID: "domain-1", RepoID: "repo-1", Kind: ReservationInteractive,
		Estimate: est, BasePrice: 1.0, ScopeGlobs: []string{"**"},
	}
}

// stepClock is a steppable injected Clock (no bare time.Now, no sleep).
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func newStepClock() *stepClock { return &stepClock{t: newTestClock().t} }

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *stepClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// hasIdentity reports whether target appears in err's chain by pointer
// identity, walking joined errors too. errors.Is is not enough here:
// cascade.Error.Is matches any error of the same Kind.
func hasIdentity(err, target error) bool {
	if err == nil {
		return false
	}
	if err == target {
		return true
	}
	switch u := err.(type) {
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			if hasIdentity(e, target) {
				return true
			}
		}
	case interface{ Unwrap() error }:
		return hasIdentity(u.Unwrap(), target)
	}
	return false
}

// requireSentinel fails unless err carries sentinel by identity and its
// message contains every want substring.
func requireSentinel(t *testing.T, err, sentinel error, wants ...string) {
	t.Helper()
	if !hasIdentity(err, sentinel) {
		t.Fatalf("err = %v, want the %q sentinel by identity", err, sentinel)
	}
	for _, w := range wants {
		if !strings.Contains(err.Error(), w) {
			t.Fatalf("err = %q, want it to name %q", err.Error(), w)
		}
	}
}

// isKindInvalidInput reports whether err carries cascade.KindInvalidInput.
func isKindInvalidInput(err error) bool { return cascade.HasKind(err, cascade.KindInvalidInput) }

// topoFixture is a real topology database: accounts and domains through
// topology.Store, buckets only through QuotaStore.UpsertBucket.
type topoFixture struct {
	db     *sql.DB
	store  *topology.Store
	quotas *topology.QuotaStore
}

func newTopoFixture(t *testing.T) *topoFixture {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "topology.db"))
	if err != nil {
		t.Fatalf("open topology db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := topology.ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("topology.ApplyMigrationSchema: %v", err)
	}
	return &topoFixture{db: db, store: topology.NewStore(db), quotas: topology.NewQuotaStore(db)}
}

// seedDomain writes one account (with role) and one quota domain.
func (tf *topoFixture) seedDomain(t *testing.T, acct topology.AccountID, role topology.AccountRole, dom topology.DomainID, kind topology.QuotaDomainKind) {
	t.Helper()
	ctx := context.Background()
	a := topology.Account{ID: acct, Provider: "prov-a", Billing: topology.BillingInfo{Kind: topology.BillingAPI}, Role: role}
	if err := tf.store.UpsertAccount(ctx, a); err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}
	d := topology.QuotaDomain{ID: dom, AccountRef: acct, Kind: kind, BillingTier: topology.BillingTierPaid}
	if err := tf.store.UpsertQuotaDomain(ctx, d); err != nil {
		t.Fatalf("UpsertQuotaDomain: %v", err)
	}
}

// bucket writes one observed bucket through the production writer.
func (tf *topoFixture) bucket(t *testing.T, dom topology.DomainID, dim string, limit, capacity int64, scope topology.LimitScopeID) {
	t.Helper()
	b := topology.Bucket{
		Name: dim, Limit: limit, RemainingFraction: 1, Window: topology.BucketWindowDay,
		Source: topology.SourceProviderStatus, Confidence: 1, LimitScopeID: scope,
		CapacityObserved: capacity, WindowID: "w1", Version: 1,
	}
	if err := tf.quotas.UpsertBucket(context.Background(), dom, dim, b); err != nil {
		t.Fatalf("UpsertBucket(%s/%s): %v", dom, dim, err)
	}
}

// bucketsSeam reads the stored domain kind, its ResolveScope scope and
// its buckets, the way a composition root would bind the Buckets seam.
func (tf *topoFixture) bucketsSeam() func(context.Context, string) (topology.QuotaDomainKind, topology.LimitScopeID, map[string]topology.Bucket, error) {
	return func(ctx context.Context, domainID string) (topology.QuotaDomainKind, topology.LimitScopeID, map[string]topology.Bucket, error) {
		d, err := tf.store.GetQuotaDomain(ctx, topology.DomainID(domainID))
		if err != nil {
			return "", "", nil, err
		}
		a, err := tf.store.GetAccount(ctx, d.AccountRef)
		if err != nil {
			return "", "", nil, err
		}
		list, err := tf.quotas.ListBuckets(ctx, d.ID)
		if err != nil {
			return "", "", nil, err
		}
		out := make(map[string]topology.Bucket, len(list))
		for _, b := range list {
			out[b.Name] = b
		}
		return d.Kind, topology.ResolveScope(a, d), out, nil
	}
}

// reserveFractionSeam returns the domain account's RoleDefaults reserve.
func (tf *topoFixture) reserveFractionSeam() func(context.Context, string) (float64, error) {
	return func(ctx context.Context, domainID string) (float64, error) {
		d, err := tf.store.GetQuotaDomain(ctx, topology.DomainID(domainID))
		if err != nil {
			return 0, err
		}
		a, err := tf.store.GetAccount(ctx, d.AccountRef)
		if err != nil {
			return 0, err
		}
		return RoleDefaults(a.Role).PreserveWeeklyReserve, nil
	}
}
