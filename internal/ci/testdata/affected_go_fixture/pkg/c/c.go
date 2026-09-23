// Package c is the affected-target fixture's test-only-import leaf:
// nothing in pkg/b's own production source (b.go) imports it -- only
// pkg/b's in-package test file (b_test.go) does. This gives the D3
// reverse-dependency-walk test a real package whose only importer is a
// _test.go file, proving affectedGoTargets follows .TestImports edges,
// not just .Imports.
package c

// C returns a fixed string, mirroring pkg/a's shape.
func C() string {
	return "c"
}
