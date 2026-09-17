package sync

// Purpose (this file): version-vector comparison and the never-pruned
//   tombstone retention rule.
// SPORT: internal/sync tests (ADD) — P1-E17-W4-S38-T2.

import (
	"errors"
	"strings"
	"testing"
)

// TestVectorCompareDistinguishesDominanceFromConcurrency is the
// distinction the whole append merge rests on. A comparison that called
// concurrent writes "equal" would be right by accident and wrong the first
// time two peers wrote at once.
func TestVectorCompareDistinguishesDominanceFromConcurrency(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b map[string]uint64
		want int
	}{
		{"a dominates", map[string]uint64{"x": 2, "y": 1}, map[string]uint64{"x": 1, "y": 1}, 1},
		{"b dominates", map[string]uint64{"x": 1}, map[string]uint64{"x": 2}, -1},
		{"equal", map[string]uint64{"x": 1}, map[string]uint64{"x": 1}, 0},
		{"concurrent", map[string]uint64{"x": 2, "y": 1}, map[string]uint64{"x": 1, "y": 2}, 0},
		{"nil is an empty vector", nil, map[string]uint64{"x": 1}, -1},
		{"both nil", nil, nil, 0},
		{"a key only one side has", map[string]uint64{"x": 1}, map[string]uint64{"y": 1}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := VectorCompare(tc.a, tc.b); got != tc.want {
				t.Errorf("VectorCompare(%v, %v) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestVectorUnionTakesTheComponentWiseMax covers the carried-forward
// vector directly, including the nil cases the merge passes it.
func TestVectorUnionTakesTheComponentWiseMax(t *testing.T) {
	got := VectorUnion(map[string]uint64{"x": 3, "y": 1}, map[string]uint64{"y": 5, "z": 2})
	want := map[string]uint64{"x": 3, "y": 5, "z": 2}
	if len(got) != len(want) {
		t.Fatalf("union = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("union = %v, want %v", got, want)
		}
	}
	if u := VectorUnion(nil, nil); len(u) != 0 {
		t.Errorf("union of two empty vectors = %v, want empty", u)
	}
}

// TestErrCursorTooOldFullResync pins the refusal and, just as importantly,
// the message: the peer is not broken and retrying will not help, so the
// error has to say what will.
func TestErrCursorTooOldFullResync(t *testing.T) {
	err := CheckPeerCursor(4, 10)
	if err == nil {
		t.Fatal("a peer whose cursor predates the oldest tombstone was accepted")
	}
	if !errors.Is(err, ErrCursorTooOld) {
		t.Errorf("err = %v, want it to wrap ErrCursorTooOld", err)
	}
	msg := err.Error()
	for _, want := range []string{"4", "10", "resync"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not mention %q", msg, want)
		}
	}
}

// TestACurrentPeerCursorIsAccepted is the other half: the check must not
// refuse everybody.
func TestACurrentPeerCursorIsAccepted(t *testing.T) {
	if err := CheckPeerCursor(10, 10); err != nil {
		t.Errorf("a cursor exactly at the oldest tombstone was refused: %v", err)
	}
	if err := CheckPeerCursor(11, 10); err != nil {
		t.Errorf("a cursor past the oldest tombstone was refused: %v", err)
	}
}

// TestTombstonesNeverPruned states P1's retention in code, against the
// configuration default, so a change to it is a failing test rather than a
// silent behaviour change.
//
// The value is what makes CheckPeerCursor unreachable in practice today:
// with nothing pruned, the oldest retained tombstone is the oldest that
// ever existed, and no correctly-behaved peer's cursor can predate it. The
// check stays because the day pruning becomes possible is the day a peer
// offline across it would resurrect everything deleted in the gap.
func TestTombstonesNeverPruned(t *testing.T) {
	if tombstoneRetention != retentionNever {
		t.Fatalf("tombstone retention = %q, want %q in P1: pruning a tombstone resurrects the record "+
			"it deleted for every peer that was offline across the prune",
			tombstoneRetention, retentionNever)
	}
}

// TestVectorUnionIsCommutative is the property a hand-written case missed
// for a subtle reason worth stating: the obvious implementation copies the
// first vector wholesale and then takes `v > out[k]` from the second, which
// keeps a zero counter from the first side and drops one from the second.
// The two unions then have different key sets for the same meaning.
//
// Nobody writes a zero counter down on purpose, so no fixture had one. A
// property run over generated inputs did, on its second seed.
func TestVectorUnionIsCommutative(t *testing.T) {
	for _, tc := range []struct{ a, b map[string]uint64 }{
		{map[string]uint64{"n1": 0}, map[string]uint64{"n2": 1}},
		{map[string]uint64{"n0": 2}, map[string]uint64{"n2": 0}},
		{map[string]uint64{"x": 0}, map[string]uint64{"x": 0}},
		{nil, map[string]uint64{"y": 0}},
	} {
		ab, ba := VectorUnion(tc.a, tc.b), VectorUnion(tc.b, tc.a)
		if len(ab) != len(ba) {
			t.Fatalf("union(%v, %v) = %v but union(%v, %v) = %v", tc.a, tc.b, ab, tc.b, tc.a, ba)
		}
		for k, v := range ab {
			if ba[k] != v {
				t.Fatalf("union(%v, %v) = %v but union(%v, %v) = %v", tc.a, tc.b, ab, tc.b, tc.a, ba)
			}
		}
	}
}

// TestAZeroCounterMeansTheSameAsAbsent states the equivalence the union
// relies on, at the comparison that reads it.
func TestAZeroCounterMeansTheSameAsAbsent(t *testing.T) {
	withZero := map[string]uint64{"n1": 0, "n2": 3}
	without := map[string]uint64{"n2": 3}
	if VectorCompare(withZero, without) != 0 {
		t.Error("a zero counter changed a comparison; it means the same as an absent key")
	}
	if got := VectorUnion(withZero, nil); len(got) != 1 {
		t.Errorf("union carried a zero counter forward: %v", got)
	}
}
