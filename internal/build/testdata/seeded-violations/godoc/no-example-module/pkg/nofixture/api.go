// Package nofixture declares an exported function but ships zero Example
// tests — the gate must report the package.
package nofixture

// DoThing is exported but this package has no Example function.
func DoThing() int {
	return 1
}
