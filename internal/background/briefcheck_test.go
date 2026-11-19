package background

import (
	"errors"
	"strings"
	"testing"
)

func TestBriefRejectsOversized(t *testing.T) {
	cases := []struct {
		name string
		size int
	}{
		{"exactly 4095 bytes", 4095},
		{"exactly 4096 bytes", 4096},
		{"larger than 4096 bytes", 5000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			brief := strings.Repeat("x", tc.size)
			called := false
			cb := func(b string) bool {
				called = true
				return true
			}

			err := CheckBrief(brief, cb)

			if tc.size >= 4096 {
				if !errors.Is(err, ErrBriefTooLarge) {
					t.Errorf("expected ErrBriefTooLarge, got %v", err)
				}
				if called {
					t.Error("callback should not be called for oversized brief")
				}
			} else {
				if errors.Is(err, ErrBriefTooLarge) {
					t.Errorf("expected no ErrBriefTooLarge for size %d, got %v", tc.size, err)
				}
			}
		})
	}
}
