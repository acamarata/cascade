package sync

// Purpose (this file): the total order, and the field it must never read.
// SPORT: internal/sync tests (ADD) — P1-E17-W4-S38-T2.

import "testing"

// TestConfigOrderingRevisionHLCNodeID pins the three-level order and its
// precedence. Getting the precedence wrong is silent: every test that uses
// only one level still passes.
func TestConfigOrderingRevisionHLCNodeID(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b OrderKey
		want int
	}{
		{"revision decides first", OrderKey{Revision: 2, HLC: 1, NodeID: "a"}, OrderKey{Revision: 1, HLC: 9, NodeID: "z"}, 1},
		{"hlc decides at equal revision", OrderKey{Revision: 2, HLC: 5, NodeID: "a"}, OrderKey{Revision: 2, HLC: 4, NodeID: "z"}, 1},
		{"node id is the last resort", OrderKey{Revision: 2, HLC: 5, NodeID: "b"}, OrderKey{Revision: 2, HLC: 5, NodeID: "a"}, 1},
		{"identical", OrderKey{Revision: 2, HLC: 5, NodeID: "a"}, OrderKey{Revision: 2, HLC: 5, NodeID: "a"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Compare(tc.a, tc.b); sgn(got) != tc.want {
				t.Errorf("Compare = %d, want sign %d", got, tc.want)
			}
			if got := Compare(tc.b, tc.a); sgn(got) != -tc.want {
				t.Errorf("Compare reversed = %d, want sign %d; the order is not antisymmetric", got, -tc.want)
			}
		})
	}
}

// TestConfigClockSkewServerWins is the reason wall time is not in the
// comparison. A laptop whose clock is a day fast must not win against the
// server, and this asserts it with a 24h skew.
func TestConfigClockSkewServerWins(t *testing.T) {
	const day = 86400
	server := OrderKey{Revision: 7, HLC: 40, NodeID: "server", WallClock: 1_700_000_000}
	skewedLaptop := OrderKey{Revision: 6, HLC: 39, NodeID: "laptop", WallClock: 1_700_000_000 + day}

	if Compare(server, skewedLaptop) <= 0 {
		t.Fatal("a laptop 24h ahead outranked the server; wall time is being compared")
	}
}

// TestConfigRollbackEqualClocks covers the two cases a clock cannot
// distinguish: a peer whose clock went BACKWARDS, and two writes at exactly
// the same instant.
func TestConfigRollbackEqualClocks(t *testing.T) {
	// Rolled back: the wall clock is earlier, the revision is not.
	rolled := OrderKey{Revision: 9, HLC: 50, NodeID: "peer", WallClock: 1}
	server := OrderKey{Revision: 8, HLC: 49, NodeID: "server", WallClock: 1_700_000_000}
	if Compare(rolled, server) <= 0 {
		t.Error("a rolled-back clock lost a comparison it should have won on revision")
	}

	// Equal clocks, equal revisions, equal HLC: the node id must still
	// produce one deterministic answer, or two peers merging the same pair
	// disagree.
	a := OrderKey{Revision: 3, HLC: 3, NodeID: "alpha", WallClock: 42}
	b := OrderKey{Revision: 3, HLC: 3, NodeID: "beta", WallClock: 42}
	if Compare(a, b) == 0 {
		t.Fatal("two distinct writers compared equal; the merge would be non-deterministic")
	}
	if sgn(Compare(a, b)) != -sgn(Compare(b, a)) {
		t.Error("the tie-break is not antisymmetric")
	}
}

// TestOrderingIgnoresWallClockEntirely is the direct statement: two keys
// differing ONLY in wall clock must compare equal.
func TestOrderingIgnoresWallClockEntirely(t *testing.T) {
	a := OrderKey{Revision: 1, HLC: 1, NodeID: "n", WallClock: 0}
	b := OrderKey{Revision: 1, HLC: 1, NodeID: "n", WallClock: 1_900_000_000}
	if Compare(a, b) != 0 {
		t.Fatal("wall clock changed the comparison")
	}
}

// sgn reduces a comparison to -1, 0 or 1.
func sgn(v int) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	default:
		return 0
	}
}
