package evidence

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestLocatorRoundTrip(t *testing.T) {
	cases := []string{
		"lines:1-10",
		"lines:0-0",
		"bytes:0-16384",
		"bytes:5-5",
		"artifact://ART-abc123",
	}
	for _, s := range cases {
		t.Run(s, func(t *testing.T) {
			loc, err := ParseLocator(s)
			if err != nil {
				t.Fatalf("ParseLocator(%q): %v", s, err)
			}
			if got := loc.String(); got != s {
				t.Errorf("round trip: got %q, want %q", got, s)
			}
		})
	}
}

func TestLocatorParseInvalid(t *testing.T) {
	cases := []string{
		"",
		"lines:",
		"lines:5-3",     // inverted
		"lines:-1-5",    // negative bound
		"bytes:abc-def", // not numeric
		"bytes:5",       // no dash
		"foo://bar",     // unknown scheme
		"artifact://",   // empty ref
		"lines5-3",      // missing colon
	}
	for _, s := range cases {
		t.Run(s, func(t *testing.T) {
			if _, err := ParseLocator(s); !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Errorf("ParseLocator(%q) = %v, want typed invalid-input", s, err)
			}
		})
	}
}

func TestLocatorKindValid(t *testing.T) {
	for _, k := range []LocatorKind{LocatorLines, LocatorBytes, LocatorArtifact} {
		if !k.Valid() {
			t.Errorf("%q.Valid() = false, want true", k)
		}
	}
	if LocatorKind("bogus").Valid() {
		t.Error("bogus kind reported valid")
	}
}

func TestLocatorZeroValueStringIsEmpty(t *testing.T) {
	var l Locator
	if got := l.String(); got != "" {
		t.Errorf("zero-value Locator.String() = %q, want \"\"", got)
	}
}

func TestLocatorJSONRoundTrip(t *testing.T) {
	loc, err := ParseLocator("lines:1-5")
	if err != nil {
		t.Fatalf("ParseLocator: %v", err)
	}
	data, err := loc.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	var out Locator
	if err := out.UnmarshalJSON(data); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if out != loc {
		t.Errorf("JSON round trip: got %+v, want %+v", out, loc)
	}
	var bad Locator
	if err := bad.UnmarshalJSON([]byte(`"not-a-locator"`)); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("UnmarshalJSON(malformed) = %v, want typed invalid-input", err)
	}
	if err := bad.UnmarshalJSON([]byte(`123`)); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("UnmarshalJSON(non-string) = %v, want typed invalid-input", err)
	}
}
