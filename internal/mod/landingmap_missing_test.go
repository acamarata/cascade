package mod

import (
	"errors"
	"testing"
)

// missingMock is an in-memory registry fixture for missing-landing tests.
type missingMock struct {
	val string
	ok  bool
}

// Lookup returns the configured fixture response.
func (m *missingMock) Lookup(_ string) (string, bool) {
	if m == nil {
		panic("Lookup invoked on nil pointer")
	}
	return m.val, m.ok
}

// TestLandingMissingFailsClosed verifies that nil interfaces, typed nil
// pointers, empty registries, and ok=false returns are treated as absent
// and produce ErrMissingLanding without invoking nil lookups.
func TestLandingMissingFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		t    LandingResolver
		m    LandingResolver
	}{
		{"both nil interfaces", nil, nil},
		{"table nil interface", nil, &missingMock{val: "Crunch", ok: true}},
		{"m nil interface", &missingMock{val: "Crunch", ok: true}, nil},
		{"table typed nil", (*missingMock)(nil), &missingMock{val: "Crunch", ok: true}},
		{"m typed nil", &missingMock{val: "Crunch", ok: true}, (*missingMock)(nil)},
		{"table ok=false", &missingMock{val: "Crunch", ok: false}, &missingMock{val: "Crunch", ok: true}},
		{"m ok=false", &missingMock{val: "Crunch", ok: true}, &missingMock{val: "Crunch", ok: false}},
		{"missing wins over conflict", &missingMock{val: "Crunch", ok: false}, &missingMock{val: "Other", ok: true}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckLanding(tc.t, tc.m)
			if !errors.Is(err, ErrMissingLanding) {
				t.Fatalf("expected ErrMissingLanding, got %v", err)
			}
		})
	}
}
