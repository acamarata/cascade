//go:build postgres

// Purpose: unit tests for prefixRange — the pure function computing Scan's
//
//	[lo, hi) key range — which needs no live server. The full Get/Put/
//	Delete/Scan/Tx conformance run against a real server is
//	storetest.RunStoreTests in integration_test.go.
//
// SPORT: providers.postgres.Store/CHANGED (P1-E17-W4-S38-T4).
package postgres

import "testing"

func TestPrefixRange(t *testing.T) {
	cases := []struct {
		prefix      string
		wantLo      string
		wantHi      string
		wantBounded bool
	}{
		{"", "", "", false},
		{"a", "a", "b", true},
		{"prefix/", "prefix/", "prefix0", true},
		{"\xff", "\xff", "", false},
		{"a\xff", "a\xff", "b", true},
	}
	for _, c := range cases {
		lo, hi, ok := prefixRange(c.prefix)
		if lo != c.wantLo || hi != c.wantHi || ok != c.wantBounded {
			t.Errorf("prefixRange(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.prefix, lo, hi, ok, c.wantLo, c.wantHi, c.wantBounded)
		}
	}
}
