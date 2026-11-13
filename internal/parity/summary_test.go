package parity

import "testing"

func TestSummarizeCounts(t *testing.T) {
	m := Matrix{"parity", "parity", "gap", "waived"}
	s := Summarize(m)
	if s.Parity != 2 || s.Gap != 1 || s.Waived != 1 {
		t.Fatalf("got %+v", s)
	}
}

func TestZeroNeedsRows(t *testing.T) {
	var m Matrix
	if Summarize(m).Zero() {
		t.Fatal("empty matrix should not be zero")
	}
}

func TestZeroFalseWithGap(t *testing.T) {
	m := Matrix{"gap"}
	if Summarize(m).Zero() {
		t.Fatal("matrix with gap should not be zero")
	}
	m2 := Matrix{"parity", "waived"}
	if !Summarize(m2).Zero() {
		t.Fatal("matrix with only parity and waived should be zero")
	}
}
