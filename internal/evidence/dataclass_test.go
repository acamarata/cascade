package evidence

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestDataClassValidAndDecode(t *testing.T) {
	for _, d := range []DataClass{DataClassPublic, DataClassInternal, DataClassConfidential, DataClassSecret} {
		if !d.Valid() {
			t.Errorf("%q.Valid() = false, want true", d)
		}
		if _, err := DecodeDataClass(string(d)); err != nil {
			t.Errorf("DecodeDataClass(%q): %v", d, err)
		}
	}
	for _, raw := range []string{"", "bogus", "restricted", "local-only"} {
		if _, err := DecodeDataClass(raw); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("DecodeDataClass(%q) = %v, want typed invalid-input", raw, err)
		}
	}
}

func TestJoinReturnsMostRestrictive(t *testing.T) {
	cases := []struct {
		in   []DataClass
		want DataClass
	}{
		{[]DataClass{DataClassPublic}, DataClassPublic},
		{[]DataClass{DataClassPublic, DataClassInternal}, DataClassInternal},
		{[]DataClass{DataClassInternal, DataClassConfidential, DataClassPublic}, DataClassConfidential},
		{[]DataClass{DataClassSecret, DataClassPublic}, DataClassSecret},
		{[]DataClass{}, DataClassSecret},
		{[]DataClass{"unknown"}, DataClassSecret},
		{[]DataClass{DataClassPublic, "unknown"}, DataClassSecret},
	}
	for _, tc := range cases {
		if got := Join(tc.in...); got != tc.want {
			t.Errorf("Join(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestJoinIsCommutative(t *testing.T) {
	a := Join(DataClassConfidential, DataClassPublic, DataClassInternal)
	b := Join(DataClassPublic, DataClassInternal, DataClassConfidential)
	if a != b {
		t.Errorf("Join order-dependence: %q vs %q", a, b)
	}
}
