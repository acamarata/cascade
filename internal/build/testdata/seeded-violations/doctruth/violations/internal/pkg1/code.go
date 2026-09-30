// Package pkg1 is a minimal fixture package: one real exported symbol
// (RealFunc) that a doc citation can resolve against, so the symbol rule's
// clean path and its MissingFunc violation both have something real to
// compare to.
package pkg1

// RealFunc is the fixture's one real exported symbol.
func RealFunc() {}
