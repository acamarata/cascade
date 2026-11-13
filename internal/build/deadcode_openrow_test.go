package build

import (
	"reflect"
	"testing"
)

func TestDeadCodeAllowedByOpenRow(t *testing.T) {
	known := map[string]bool{"T-OPEN": true, "T-CLOSED": true}
	closed := map[string]bool{"T-CLOSED": true}
	viol := DeadCodeViolation{Dir: "internal/x", Name: "Y", File: "internal/x/y.go", Line: 3}
	const key = "internal/x.Y"
	row := func(ticket string) map[string]TestOnlyAllowEntry {
		return map[string]TestOnlyAllowEntry{key: {Symbol: key, Reason: "r", Caller: "c", RetireTicket: ticket}}
	}

	cases := []struct {
		name     string
		allow    map[string]TestOnlyAllowEntry
		wantHeld bool
	}{
		{"open ticket holds", row("T-OPEN"), true},
		{"closed ticket is a violation", row("T-CLOSED"), false},
		{"unowned is a violation", row(UnownedTicket), false},
		{"capability key is a violation", row("cap:something"), false},
		{"unknown id is a violation", row("T-UNKNOWN"), false},
		{"no row is a violation", map[string]TestOnlyAllowEntry{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			remaining, held := DeadCodeAllowedByOpenRow([]DeadCodeViolation{viol}, tc.allow, known, closed)
			if tc.wantHeld {
				if len(remaining) != 0 || !reflect.DeepEqual(held, []string{key}) {
					t.Fatalf("want held [%s] and no violation, got held=%v remaining=%+v", key, held, remaining)
				}
				return
			}
			if len(held) != 0 || !reflect.DeepEqual(remaining, []DeadCodeViolation{viol}) {
				t.Fatalf("want the symbol reported and nothing held, got held=%v remaining=%+v", held, remaining)
			}
		})
	}
}
