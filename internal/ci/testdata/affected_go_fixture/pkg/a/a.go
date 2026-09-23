// Package a is the affected-target fixture's leaf package: it imports
// nothing, so pkg/b (its only importer, see pkg/b/b.go) has a real
// upstream dependency to be affected BY, and a-only changes have no
// package left upstream of them to falsely mark affected.
package a

// A returns a fixed string. Its only purpose is to give pkg/b a concrete
// symbol to import, so a change to this file is a change to a real
// dependency edge, not a synthetic one.
func A() string {
	return "a"
}
