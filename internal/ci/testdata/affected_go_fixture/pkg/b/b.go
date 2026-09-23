// Package b imports pkg/a, so pkg/a is transitively "under" pkg/b: a
// change to pkg/a's file must mark BOTH pkg/a and pkg/b affected, while a
// change to pkg/b's own file must mark pkg/b alone (pkg/a has no
// dependency on pkg/b) -- the fixture's real cross-cutting-vs-isolated
// pair.
package b

import "example.com/fixture/pkg/a"

// B returns pkg/a's value with a "b:" prefix.
func B() string {
	return "b:" + a.A()
}
