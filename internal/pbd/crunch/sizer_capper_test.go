package crunch

import (
	"errors"
	"testing"
)

// TestBigModelRequestExceedingCapRejected ensures that big model requests under v2 are rejected
// with ErrBigModelCap, have the correct error text, and do not alter state or admission effects.
func TestBigModelRequestExceedingCapRejected(t *testing.T) {
	if V2BigModelCap != 0 {
		t.Fatalf("expected V2BigModelCap to be 0, got %d", V2BigModelCap)
	}

	err := CheckBigModel("big", true)
	if !errors.Is(err, ErrBigModelCap) {
		t.Fatalf("expected ErrBigModelCap, got %v", err)
	}
	if err.Error() != "P2-CRUN-05" {
		t.Fatalf("expected error text P2-CRUN-05, got %q", err.Error())
	}

	// Call again to ensure no state or admission effect
	err = CheckBigModel("big", true)
	if !errors.Is(err, ErrBigModelCap) {
		t.Fatalf("expected ErrBigModelCap on second call, got %v", err)
	}
}

// TestBigModelRequestWithinCapAllowed ensures that small v2 requests succeed,
// both valid sizes outside v2 succeed, and invalid sizes return ErrInvalidModelSize.
func TestBigModelRequestWithinCapAllowed(t *testing.T) {
	// small v2 succeeds
	if err := CheckBigModel("small", true); err != nil {
		t.Fatalf("expected small v2 to succeed, got %v", err)
	}

	// both valid sizes outside v2 succeed
	if err := CheckBigModel("small", false); err != nil {
		t.Fatalf("expected small non-v2 to succeed, got %v", err)
	}
	if err := CheckBigModel("big", false); err != nil {
		t.Fatalf("expected big non-v2 to succeed, got %v", err)
	}

	// invalid sizes
	invalids := []string{"", " big ", "huge", "BIG", "Small"}
	for _, s := range invalids {
		err := CheckBigModel(s, true)
		if !errors.Is(err, ErrInvalidModelSize) {
			t.Errorf("CheckBigModel(%q, true): expected ErrInvalidModelSize, got %v", s, err)
		}
		if err != nil && err.Error() != "invalid model size" {
			t.Errorf("CheckBigModel(%q, true): expected error text 'invalid model size', got %q", s, err.Error())
		}

		err = CheckBigModel(s, false)
		if !errors.Is(err, ErrInvalidModelSize) {
			t.Errorf("CheckBigModel(%q, false): expected ErrInvalidModelSize, got %v", s, err)
		}
		if err != nil && err.Error() != "invalid model size" {
			t.Errorf("CheckBigModel(%q, false): expected error text 'invalid model size', got %q", s, err.Error())
		}
	}
}
