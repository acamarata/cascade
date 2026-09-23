package c

import "testing"

func TestC(t *testing.T) {
	if got, want := C(), "c"; got != want {
		t.Fatalf("C() = %q, want %q", got, want)
	}
}
