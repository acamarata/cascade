package ladder

import "testing"

func TestRungFields(t *testing.T) {
	r := Rung{Name: "test", Kind: KindPrimary, ResetHours: 24, Room: true, Stale: false}
	if r.Name != "test" || r.Kind != KindPrimary || r.ResetHours != 24 || !r.Room || r.Stale {
		t.Errorf("Rung = %+v, want {test primary 24 true false}", r)
	}
}

func TestKindValues(t *testing.T) {
	want := map[string]string{
		KindPrimary:   "primary",
		KindSecondary: "secondary",
		KindTertiary:  "tertiary",
		KindWindowed:  "windowed",
		KindReserved:  "reserved",
	}
	for got, w := range want {
		if got != w {
			t.Errorf("kind = %q, want %q", got, w)
		}
	}
	if len(want) != 5 {
		t.Errorf("kinds are not distinct: %d of 5", len(want))
	}
}
