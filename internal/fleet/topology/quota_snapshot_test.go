package topology

import (
	"context"
	"errors"
	"testing"
	"time"
)

func seedFullDomain(t *testing.T, s *QuotaStore, domainID DomainID, confidences map[string]float64) {
	t.Helper()
	for name, conf := range confidences {
		b := Bucket{
			Name: name, Limit: 10, RemainingFraction: 0.5, Window: BucketWindowDay,
			Source: SourceCLIObservation, Confidence: conf, LimitScopeID: "scope:acct-1",
			CapacityObserved: UnobservedCapacity,
		}
		if err := s.UpsertBucket(context.Background(), domainID, name, b); err != nil {
			t.Fatalf("seed UpsertBucket(%s): %v", name, err)
		}
	}
}

// TestDomainConfidenceIsMinimumAcrossDimensions is the named acceptance
// criterion: a domain's snapshot confidence equals the minimum across its
// dimensions, and an unobserved dimension contributes 0.
func TestDomainConfidenceIsMinimumAcrossDimensions(t *testing.T) {
	ctx := context.Background()
	s := newQuotaTestStore(t)
	seedFullDomain(t, s, "dom-1", map[string]float64{
		DimensionRPM: 0.9, DimensionTPM: 0.3, DimensionRPD: 0.99,
	})
	domain := QuotaDomain{ID: "dom-1", AccountRef: "acct-1", Kind: QuotaDomainAPIProject}
	snap, err := TakeQuotaSnapshot(ctx, s, []QuotaDomain{domain}, time.Now())
	if err != nil {
		t.Fatalf("TakeQuotaSnapshot: %v", err)
	}
	if len(snap.Domains) != 1 {
		t.Fatalf("Domains len = %d, want 1", len(snap.Domains))
	}
	if got, want := snap.Domains[0].Confidence, 0.3; got != want {
		t.Errorf("Confidence = %v, want the minimum %v", got, want)
	}
}

func TestDomainConfidenceZeroWhenDimensionUnobserved(t *testing.T) {
	ctx := context.Background()
	s := newQuotaTestStore(t)
	// Only rpm and tpm observed; rpd (part of api_project's closed set)
	// never observed.
	seedFullDomain(t, s, "dom-1", map[string]float64{DimensionRPM: 0.9, DimensionTPM: 0.9})
	domain := QuotaDomain{ID: "dom-1", AccountRef: "acct-1", Kind: QuotaDomainAPIProject}
	snap, err := TakeQuotaSnapshot(ctx, s, []QuotaDomain{domain}, time.Now())
	if err != nil {
		t.Fatalf("TakeQuotaSnapshot: %v", err)
	}
	if got := snap.Domains[0].Confidence; got != 0 {
		t.Errorf("Confidence with an unobserved dimension = %v, want 0", got)
	}
}

func TestTakeQuotaSnapshotAppendsRow(t *testing.T) {
	ctx := context.Background()
	s := newQuotaTestStore(t)
	domain := QuotaDomain{ID: "dom-1", AccountRef: "acct-1", Kind: QuotaDomainSharedPool}
	// Two calls must each succeed independently -- AppendSnapshot mints a
	// fresh id per call (idMinter), so back-to-back snapshots never
	// collide on the append-only table's primary key.
	if _, err := TakeQuotaSnapshot(ctx, s, []QuotaDomain{domain}, time.Now()); err != nil {
		t.Fatalf("first TakeQuotaSnapshot: %v", err)
	}
	if _, err := TakeQuotaSnapshot(ctx, s, []QuotaDomain{domain}, time.Now()); err != nil {
		t.Fatalf("second TakeQuotaSnapshot: %v", err)
	}
}

// TestTakeQuotaSnapshotRejectsDimensionOutsideKind proves buildDomainQuota's
// real production use of ValidateDimensions: a stored bucket whose
// dimension name does not belong to the domain's Kind fails the whole
// snapshot closed, rather than silently being reported as a well-known
// dimension of the wrong kind.
func TestTakeQuotaSnapshotRejectsDimensionOutsideKind(t *testing.T) {
	ctx := context.Background()
	s := newQuotaTestStore(t)
	// weekly is valid for shared_pool, never for api_project.
	b := Bucket{Name: DimensionWeekly, Limit: 10, RemainingFraction: 0.5, Window: BucketWindowWeek, Source: SourceCLIObservation, Confidence: 0.5, LimitScopeID: "scope:acct-1", CapacityObserved: UnobservedCapacity}
	if err := s.UpsertBucket(ctx, "dom-1", DimensionWeekly, b); err != nil {
		t.Fatalf("seed UpsertBucket: %v", err)
	}
	domain := QuotaDomain{ID: "dom-1", AccountRef: "acct-1", Kind: QuotaDomainAPIProject}
	if _, err := TakeQuotaSnapshot(ctx, s, []QuotaDomain{domain}, time.Now()); !errors.Is(err, ErrTopologyInvariant) {
		t.Errorf("TakeQuotaSnapshot with a mismatched dimension: err = %v, want ErrTopologyInvariant", err)
	}
}

func TestTakeQuotaSnapshotRequiresStore(t *testing.T) {
	if _, err := TakeQuotaSnapshot(context.Background(), nil, nil, time.Now()); err == nil {
		t.Fatal("TakeQuotaSnapshot(nil store) should have failed")
	}
}

func TestSnapshotWithMinterPropagatesMintError(t *testing.T) {
	s := newQuotaTestStore(t)
	failingMint := func() (string, error) { return "", errBoom }
	if _, err := snapshotWithMinter(context.Background(), s, nil, time.Now(), failingMint); !errors.Is(err, errBoom) {
		t.Errorf("expected the mint error to propagate, got %v", err)
	}
}

var errBoom = errors.New("boom")
