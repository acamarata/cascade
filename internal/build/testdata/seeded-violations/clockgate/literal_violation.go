// Package violation is a seeded-violation fixture for the AST clock gate
// (R-14.132). This file is never built or linted — it lives under
// testdata/, skipped by every gate in this package (Art.1/Art.7.1).
package violation

import "time"

func BareNow() time.Time {
	return time.Now()
}
