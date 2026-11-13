package parity

import "testing"

// statuses builds a Matrix whose rows carry the given statuses, in order.
func statuses(st ...string) Matrix {
	m := Matrix{}
	for i, s := range st {
		m.Rows = append(m.Rows, Row{ID: string(rune('a' + i)), Status: s})
	}
	return m
}

func TestSummarizeCounts(t *testing.T) {
	s := Summarize(statuses("parity", "parity", "gap", "waived"))
	if s.Parity != 2 || s.Gap != 1 || s.Waived != 1 {
		t.Fatalf("got %+v", s)
	}
}

func TestZeroNeedsRows(t *testing.T) {
	if Summarize(Matrix{}).Zero() {
		t.Fatal("empty matrix should not be zero")
	}
}

func TestZeroFalseWithGap(t *testing.T) {
	if Summarize(statuses("gap")).Zero() {
		t.Fatal("matrix with gap should not be zero")
	}
	if !Summarize(statuses("parity", "waived")).Zero() {
		t.Fatal("matrix with only parity and waived should be zero")
	}
}
