// Package cyclea is one half of the seeded-violation fixture for the
// internal/build arch test that proves the module's internal import graph
// has no cycles (12-QUALITY-CONSTITUTION.md Art.10.2). cyclea imports
// cycleb and cycleb imports cyclea back, deliberately. It lives under
// testdata/ so the Go toolchain never compiles it into the product
// (Art.1/Art.7.1) — a real cycle here would fail to build anyway; the arch
// test parses the import declarations directly without building, under the
// fixture's own fake module prefix (example.com/archfixture), never the
// real cascade module path.
//
// Seeded violation — do not "fix" this file; it is the fixture.
package cyclea

import (
	_ "example.com/archfixture/internal/cycleb"
)
