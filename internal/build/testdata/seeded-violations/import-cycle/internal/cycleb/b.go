// Package cycleb is the other half of the seeded-violation fixture for the
// internal/build arch test that proves the module's internal import graph
// has no cycles (12-QUALITY-CONSTITUTION.md Art.10.2). See cyclea/a.go for
// the full explanation.
//
// Seeded violation — do not "fix" this file; it is the fixture.
package cycleb

import (
	_ "example.com/archfixture/internal/cyclea"
)
