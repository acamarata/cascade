package topology

import (
	"errors"
	"testing"
	"time"
)

func validBucket() Bucket {
	return Bucket{
		Name:              "rpm",
		Limit:             100,
		RemainingFraction: 0.5,
		Window:            BucketWindowRolling,
		Source:            SourceCLIObservation,
		Confidence:        0.8,
		CapacityObserved:  UnobservedCapacity,
	}
}

func TestNewBucketAcceptsValid(t *testing.T) {
	if _, err := NewBucket(validBucket()); err != nil {
		t.Fatalf("NewBucket(valid) error = %v, want nil", err)
	}
}

func TestNewBucketRejectsEmptyName(t *testing.T) {
	b := validBucket()
	b.Name = ""
	if _, err := NewBucket(b); !errors.Is(err, ErrTopologyInvariant) {
		t.Errorf("empty name: err = %v, want ErrTopologyInvariant", err)
	}
}

func TestNewBucketRejectsOutOfRangeRemainingFraction(t *testing.T) {
	for _, v := range []float64{-0.01, 1.01, -1, 2} {
		b := validBucket()
		b.RemainingFraction = v
		if _, err := NewBucket(b); !errors.Is(err, ErrTopologyInvariant) {
			t.Errorf("remaining_fraction=%v: err = %v, want ErrTopologyInvariant (never clamped)", v, err)
		}
	}
}

func TestNewBucketRejectsOutOfRangeConfidence(t *testing.T) {
	for _, v := range []float64{-0.5, 1.5} {
		b := validBucket()
		b.Confidence = v
		if _, err := NewBucket(b); !errors.Is(err, ErrTopologyInvariant) {
			t.Errorf("confidence=%v: err = %v, want ErrTopologyInvariant (never clamped)", v, err)
		}
	}
}

func TestNewBucketRejectsUnrecognisedWindow(t *testing.T) {
	b := validBucket()
	b.Window = BucketWindow("fortnight")
	if _, err := NewBucket(b); !errors.Is(err, ErrTopologyInvariant) {
		t.Errorf("bad window: err = %v, want ErrTopologyInvariant", err)
	}
}

func TestNewBucketRejectsUnrecognisedSource(t *testing.T) {
	b := validBucket()
	b.Source = BucketSource("rumor")
	if _, err := NewBucket(b); !errors.Is(err, ErrTopologyInvariant) {
		t.Errorf("bad source: err = %v, want ErrTopologyInvariant", err)
	}
}

// TestUnknownAndDiscoverBucketsAreNeverUnlimited is the named acceptance-
// criteria test: an unknown-source bucket and a DiscoverLimit (-1) bucket
// are both Reservable and neither ever reads as unlimited capacity -- only
// a bucket whose Limit is exactly 0 refuses.
func TestUnknownAndDiscoverBucketsAreNeverUnlimited(t *testing.T) {
	unknown := validBucket()
	unknown.Source = SourceUnknown
	unknown.Limit = 50
	if !Reservable(unknown, 10) {
		t.Error("SourceUnknown bucket with a positive limit should be Reservable")
	}

	discover := validBucket()
	discover.Limit = DiscoverLimit
	if !Reservable(discover, 10_000_000) {
		t.Error("DiscoverLimit (-1) bucket should be Reservable regardless of estimate")
	}

	zero := validBucket()
	zero.Limit = 0
	if Reservable(zero, 1) {
		t.Error("Limit=0 bucket must refuse a reservation (the only refusal case)")
	}

	// A known positive limit still refuses an estimate that could never
	// fit even at full capacity -- Reservable is not a no-op gate.
	tooBig := validBucket()
	tooBig.Limit = 10
	if Reservable(tooBig, 11) {
		t.Error("estimate exceeding a known positive limit should refuse")
	}
}

func TestBucketWindowValid(t *testing.T) {
	for _, w := range []BucketWindow{BucketWindow5h, BucketWindowDay, BucketWindowWeek, BucketWindowMonth, BucketWindowRolling} {
		if !w.Valid() {
			t.Errorf("%q should be valid", w)
		}
	}
	if BucketWindow("").Valid() {
		t.Error("zero value BucketWindow should be invalid")
	}
}

func TestBucketSourcePrecedenceOrder(t *testing.T) {
	if SourceProviderStatus.precedence() <= SourceCLIObservation.precedence() {
		t.Error("provider-status must outrank cli-observation")
	}
	if SourceCLIObservation.precedence() <= SourceUserEstimate.precedence() {
		t.Error("cli-observation must outrank user-estimate")
	}
	if SourceUserEstimate.precedence() <= SourceUnknown.precedence() {
		t.Error("user-estimate must outrank unknown")
	}
	if BucketSource("bogus").precedence() != -1 {
		t.Error("an invalid source should report precedence -1")
	}
}

func TestNewBucketObservedAtAndResetAtPassThrough(t *testing.T) {
	b := validBucket()
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	b.ObservedAt = now
	b.ResetAt = now.Add(time.Hour)
	got, err := NewBucket(b)
	if err != nil {
		t.Fatalf("NewBucket: %v", err)
	}
	if !got.ObservedAt.Equal(now) || !got.ResetAt.Equal(now.Add(time.Hour)) {
		t.Error("NewBucket must not mutate timestamps")
	}
}
