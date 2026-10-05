package topology

import (
	"context"
	"testing"
)

// TestUpsertBucketRefusesForeignDimension: the production bucket writer
// reads the stored domain kind and refuses a dimension outside that
// kind's closed set (ErrTopologyInvariant) and a domain that does not
// exist (ErrTopologyNotFound); nothing is written either way.
func TestUpsertBucketRefusesForeignDimension(t *testing.T) {
	ctx := context.Background()
	s := newQuotaTestStore(t)
	weekly := sampleBucket(DimensionWeekly)
	err := s.UpsertBucket(ctx, "dom-1", DimensionWeekly, weekly)
	if !isSentinel(err, ErrTopologyInvariant) {
		t.Fatalf("UpsertBucket(weekly on api_project) = %v, want ErrTopologyInvariant", err)
	}
	tokens := sampleBucket("tokens_in")
	if err := s.UpsertBucket(ctx, "dom-1", "tokens_in", tokens); !isSentinel(err, ErrTopologyInvariant) {
		t.Fatalf("UpsertBucket(tokens_in) = %v, want ErrTopologyInvariant", err)
	}
	if err := s.UpsertBucket(ctx, "dom-missing", DimensionRPD, sampleBucket(DimensionRPD)); !isSentinel(err, ErrTopologyNotFound) {
		t.Fatalf("UpsertBucket(missing domain) = %v, want ErrTopologyNotFound", err)
	}
	for _, dom := range []DomainID{"dom-1", "dom-missing"} {
		if list, err := s.ListBuckets(ctx, dom); err != nil || len(list) != 0 {
			t.Errorf("ListBuckets(%s) = %v, %v, want nothing written", dom, list, err)
		}
	}
	if err := s.UpsertBucket(ctx, "dom-1", DimensionRPD, sampleBucket(DimensionRPD)); err != nil {
		t.Fatalf("UpsertBucket(rpd on api_project) = %v, want accepted", err)
	}
}

// isSentinel reports whether target is in err's chain by pointer
// identity (errors.Is on cascade errors compares Kind only).
func isSentinel(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
