package docket

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateStatusWithinLimit(t *testing.T) {
	tests := []struct {
		name string
		s    string
	}{
		{"empty", ""},
		{"50 bytes", strings.Repeat("a", 50)},
		{"255 bytes", strings.Repeat("a", 255)},
		{"multibyte <=255 bytes", strings.Repeat("é", 127)}, // 127 * 2 = 254 bytes
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateStatus(tc.s); err != nil {
				t.Errorf("ValidateStatus(%q) = %v, want nil", tc.s, err)
			}
		})
	}
	// zero LeanBrief validates
	b := LeanBrief{}
	if err := Validate(b); err != nil {
		t.Errorf("Validate(empty LeanBrief) = %v, want nil", err)
	}
}

func TestValidateStatusExceedsLimit(t *testing.T) {
	s := strings.Repeat("a", 256)
	if err := ValidateStatus(s); !errors.Is(err, ErrStatusTooLong) {
		t.Errorf("ValidateStatus(256 bytes) = %v, want %v", err, ErrStatusTooLong)
	}
	if err := ValidateStatus(s); err.Error() != "D1441: status exceeds 255 bytes" {
		t.Errorf("ErrStatusTooLong message = %q, want %q", err.Error(), "D1441: status exceeds 255 bytes")
	}

	// multibyte example measured in bytes
	sMulti := strings.Repeat("é", 128) // 128 * 2 = 256 bytes
	if err := ValidateStatus(sMulti); !errors.Is(err, ErrStatusTooLong) {
		t.Errorf("ValidateStatus(multibyte 256 bytes) = %v, want %v", err, ErrStatusTooLong)
	}

	// Validate returns it first when every field is over its bound
	b := LeanBrief{
		Status: strings.Repeat("a", 256),
		Size:   10001,
		Deps:   []string{"x"},
	}
	if err := Validate(b); !errors.Is(err, ErrStatusTooLong) {
		t.Errorf("Validate(all over bound) = %v, want %v", err, ErrStatusTooLong)
	}
}

func TestValidateSizeWithinLimit(t *testing.T) {
	sizes := []int{-1, 0, 100, 10000}
	for _, n := range sizes {
		if err := ValidateSize(n); err != nil {
			t.Errorf("ValidateSize(%d) = %v, want nil", n, err)
		}
	}
}

func TestValidateSizeExceedsLimit(t *testing.T) {
	n := 10001
	if err := ValidateSize(n); !errors.Is(err, ErrSizeTooLarge) {
		t.Errorf("ValidateSize(%d) = %v, want %v", n, err, ErrSizeTooLarge)
	}
	if err := ValidateSize(n); err.Error() != "D1441: size exceeds 10000" {
		t.Errorf("ErrSizeTooLarge message = %q, want %q", err.Error(), "D1441: size exceeds 10000")
	}

	// Validate returns it before dependency error when status passes
	b := LeanBrief{
		Status: "ok",
		Size:   10001,
		Deps:   []string{"x"},
	}
	if err := Validate(b); !errors.Is(err, ErrSizeTooLarge) {
		t.Errorf("Validate(size over limit) = %v, want %v", err, ErrSizeTooLarge)
	}
}
