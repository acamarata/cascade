package pbdmode

import "testing"

func TestSlugLower(t *testing.T) {
	cases := map[string]string{
		"P3-UI":     "ui",
		"P12-GAPS":  "gaps",
		"P2-PLN-23": "pln-23",
		"NODASH":    "",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}
