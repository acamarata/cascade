// Package hasfixture declares an exported function AND ships an Example —
// the gate must NOT report this package.
package hasfixture

// DoThing is exported and demonstrated by ExampleDoThing below.
func DoThing() int {
	return 1
}
