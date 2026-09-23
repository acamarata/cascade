package a

import "testing"

func TestA(t *testing.T) {
	if got, want := A(), "a"; got != want {
		t.Fatalf("A() = %q, want %q", got, want)
	}
}
