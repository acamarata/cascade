// pkg/b's in-package test imports pkg/c -- the fixture's D3 edge:
// pkg/c's ONLY importer is this _test.go file, never pkg/b.go itself.
package b

import (
	"testing"

	"example.com/fixture/pkg/c"
)

func TestB(t *testing.T) {
	if got, want := B(), "b:a"; got != want {
		t.Fatalf("B() = %q, want %q", got, want)
	}
	if got, want := c.C(), "c"; got != want {
		t.Fatalf("c.C() = %q, want %q", got, want)
	}
}
