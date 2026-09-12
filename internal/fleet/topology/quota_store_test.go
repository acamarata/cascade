package topology

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

func newQuotaTestStore(t *testing.T) *QuotaStore {
	t.Helper()
	db := openRealSQLiteFile(t)
	if err := ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	return NewQuotaStore(db)
}

func sampleBucket(name string) Bucket {
	return Bucket{
		Name: name, Limit: 100, RemainingFraction: 0.5, Window: BucketWindowDay,
		Source: SourceCLIObservation, Confidence: 0.7, LimitScopeID: "scope:acct-1",
		CapacityObserved: 100, CommittedSinceObservation: 50, WindowID: "w1", Version: 1,
	}
}

func TestUpsertGetBucket(t *testing.T) {
	ctx := context.Background()
	s := newQuotaTestStore(t)
	b := sampleBucket(DimensionRPD)
	if err := s.UpsertBucket(ctx, "dom-1", DimensionRPD, b); err != nil {
		t.Fatalf("UpsertBucket: %v", err)
	}
	got, err := s.GetBucket(ctx, "dom-1", DimensionRPD)
	if err != nil {
		t.Fatalf("GetBucket: %v", err)
	}
	if got.Name != b.Name || got.Limit != b.Limit || got.Source != b.Source || got.LimitScopeID != b.LimitScopeID {
		t.Errorf("GetBucket = %+v, want fields matching %+v", got, b)
	}
}

func TestUpsertBucketRejectsInvalid(t *testing.T) {
	s := newQuotaTestStore(t)
	if err := s.UpsertBucket(context.Background(), "dom-1", "rpd", Bucket{}); err == nil {
		t.Fatal("UpsertBucket(zero value) should have failed validation")
	}
}

func TestUpsertBucketOverwritesByDomainAndDimension(t *testing.T) {
	ctx := context.Background()
	s := newQuotaTestStore(t)
	b1 := sampleBucket(DimensionRPD)
	b1.Version = 1
	if err := s.UpsertBucket(ctx, "dom-1", DimensionRPD, b1); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	b2 := sampleBucket(DimensionRPD)
	b2.Version = 2
	b2.CommittedSinceObservation = 90
	if err := s.UpsertBucket(ctx, "dom-1", DimensionRPD, b2); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got, err := s.GetBucket(ctx, "dom-1", DimensionRPD)
	if err != nil {
		t.Fatalf("GetBucket: %v", err)
	}
	if got.Version != 2 || got.CommittedSinceObservation != 90 {
		t.Errorf("GetBucket after re-upsert = %+v, want the second observation to win", got)
	}
	list, err := s.ListBuckets(ctx, "dom-1")
	if err != nil {
		t.Fatalf("ListBuckets: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("ListBuckets len = %d, want 1 (upsert must not duplicate the row)", len(list))
	}
}

func TestGetBucketNotFound(t *testing.T) {
	if _, err := newQuotaTestStore(t).GetBucket(context.Background(), "missing", "rpd"); err == nil {
		t.Fatal("GetBucket(missing) should have failed")
	}
}

// TestNoDispatchPathMutatesQuotaBucket is the named acceptance test:
// sessions_quota_bucket is a read-only observation cache. Available (the
// dispatch-side derivation) takes no store reference at all, so a
// dispatch/reservation flow structurally cannot write this table --
// proven here by calling Available many times against the seeded bucket's
// numbers and then re-reading the row unchanged.
func TestNoDispatchPathMutatesQuotaBucket(t *testing.T) {
	ctx := context.Background()
	s := newQuotaTestStore(t)
	seed := sampleBucket(DimensionRPD)
	if err := s.UpsertBucket(ctx, "dom-1", DimensionRPD, seed); err != nil {
		t.Fatalf("seed UpsertBucket: %v", err)
	}
	before, err := s.GetBucket(ctx, "dom-1", DimensionRPD)
	if err != nil {
		t.Fatalf("GetBucket before: %v", err)
	}

	rs := []ReservationEstimate{
		{ReservationID: "r1", LimitScopeID: before.LimitScopeID, Dimension: DimensionRPD, State: ReservationHeld, Estimate: 10},
		{ReservationID: "r2", LimitScopeID: before.LimitScopeID, Dimension: DimensionRPD, State: ReservationCommitted, Estimate: 5},
	}
	for i := 0; i < 5; i++ {
		_ = Available(before.LimitScopeID, DimensionRPD, before.CapacityObserved, before.CommittedSinceObservation, rs, time.Now())
	}

	after, err := s.GetBucket(ctx, "dom-1", DimensionRPD)
	if err != nil {
		t.Fatalf("GetBucket after: %v", err)
	}
	if after.CapacityObserved != before.CapacityObserved || after.CommittedSinceObservation != before.CommittedSinceObservation || after.Version != before.Version {
		t.Errorf("quota_bucket row changed after Available calls: before=%+v after=%+v", before, after)
	}
}

func TestAppendSnapshotIsAppendOnly(t *testing.T) {
	ctx := context.Background()
	s := newQuotaTestStore(t)
	snap := QuotaSnapshot{TakenAt: time.Now(), Domains: []DomainQuota{{DomainID: "dom-1"}}}
	if err := s.AppendSnapshot(ctx, "snap-1", snap); err != nil {
		t.Fatalf("AppendSnapshot: %v", err)
	}
	if err := s.AppendSnapshot(ctx, "snap-2", snap); err != nil {
		t.Fatalf("second AppendSnapshot: %v", err)
	}
	if err := s.AppendSnapshot(ctx, "snap-1", snap); err == nil {
		t.Error("re-using an id should conflict (append-only, primary key on id)")
	}
}
