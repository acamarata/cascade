package rates

import (
	"testing"
	"time"
)

func TestNewTracker(t *testing.T) {
	tr := NewTracker("rates.json")
	if tr == nil {
		t.Fatal("NewTracker returned nil")
	}
}

func TestConstants(t *testing.T) {
	if UpdateInterval != 15*time.Second {
		t.Fatalf("UpdateInterval = %v, want %v", UpdateInterval, 15*time.Second)
	}
}

func TestErrors(t *testing.T) {
	if ErrRatesIO == nil {
		t.Fatal("ErrRatesIO is nil")
	}
	if ErrRatesIO.Error() != "D2533: rates I/O failed" {
		t.Fatalf("ErrRatesIO = %q, want %q", ErrRatesIO.Error(), "D2533: rates I/O failed")
	}
	if ErrInvalidRates == nil {
		t.Fatal("ErrInvalidRates is nil")
	}
	if ErrInvalidRates.Error() != "invalid rates" {
		t.Fatalf("ErrInvalidRates = %q, want %q", ErrInvalidRates.Error(), "invalid rates")
	}
}

func TestBucketStruct(t *testing.T) {
	b := Bucket{Start: 123, Count: 456}
	if b.Start != 123 || b.Count != 456 {
		t.Fatal("Bucket struct failed")
	}
}

func TestRatesStruct(t *testing.T) {
	r := Rates{
		Minutes: []Bucket{{Start: 1, Count: 2}},
		Hours:   []Bucket{{Start: 3, Count: 4}},
	}
	if len(r.Minutes) != 1 || r.Minutes[0].Start != 1 {
		t.Fatal("Rates.Minutes failed")
	}
	if len(r.Hours) != 1 || r.Hours[0].Start != 3 {
		t.Fatal("Rates.Hours failed")
	}
}
