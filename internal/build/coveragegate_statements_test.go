package build

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDiscoverDirNeedsStatements pins which packages coverage can measure.
// A package of only types, consts and plain vars writes no profile lines,
// so it must not be expected (it would read as "missing"); a function
// body or a closure in a var makes it measurable again.
func TestDiscoverDirNeedsStatements(t *testing.T) {
	cases := []struct {
		name, src string
		want      bool
	}{
		{"types and consts only", "package p\n\ntype State int\n\nconst (\n\tA State = iota\n\tB\n)\n", false},
		{"plain vars only", "package p\n\nvar Names = []string{\"a\", \"b\"}\n", false},
		{"doc only", "// Package p is a scaffold.\npackage p\n", false},
		{"function body", "package p\n\nfunc F() int { return 1 }\n", true},
		{"method body", "package p\n\ntype T struct{}\n\nfunc (T) M() {}\n", true},
		{"closure in a var", "package p\n\nvar F = func() int { return 1 }\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "p.go"), []byte(tc.src), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := discoverDirHasShippableGoFile(dir)
			if err != nil {
				t.Fatalf("discoverDirHasShippableGoFile: %v", err)
			}
			if got != tc.want {
				t.Fatalf("measurable = %v, want %v", got, tc.want)
			}
		})
	}
}
