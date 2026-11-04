package build

import "testing"

// TestTestOnlyGateLetteredTicketIDs pins the ticket-id grammar of the
// test-only allow gate: one optional lowercase split letter after the
// number, nothing wider.
func TestTestOnlyGateLetteredTicketIDs(t *testing.T) {
	cases := []struct {
		id     string
		retire bool
		pewtt  bool
	}{
		{"P1-TOP-13d", true, true},
		{"P1-AGT-08m", true, true},
		{"P1-CI-37", true, true},
		{"P12-ABC-123b", true, true},
		{"P1-TOP-13dd", false, false},
		{"P1-TOP-13D", false, false},
		{"P1-TOP-13-d", false, false},
		{"P1-top-13d", false, false},
		{"cap:foo-bar", true, false},
	}
	for _, c := range cases {
		if got := retireKeyPattern.MatchString(c.id); got != c.retire {
			t.Errorf("retireKeyPattern.MatchString(%q) = %v, want %v", c.id, got, c.retire)
		}
		if got := pewttIDPattern.MatchString(c.id); got != c.pewtt {
			t.Errorf("pewttIDPattern.MatchString(%q) = %v, want %v", c.id, got, c.pewtt)
		}
	}
}
