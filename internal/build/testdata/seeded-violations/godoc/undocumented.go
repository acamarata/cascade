// Package violation is a seeded-violation fixture for the pkg/ godoc gate
// (Art.10.6). Never built or linted — lives under testdata/.
package violation

func UndocumentedFunc() int {
	return 1
}

type UndocumentedType struct{}

// DocumentedFunc has a proper godoc comment and must not be flagged.
func DocumentedFunc() int {
	return 2
}
