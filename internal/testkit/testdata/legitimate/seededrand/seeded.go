// Package seededrand is a legitimate-use fixture proving the
// no-unseeded-rand rule does not false-positive on properly seeded,
// injected randomness (Art.7.3) — see badtime/violation.go for the
// fixture-data discipline this mirrors.
package seededrand

import "math/rand"

// RollDie takes an explicitly seeded *rand.Rand instead of touching the
// global source. Its method calls (r.Intn) have a variable receiver, not
// the package identifier "rand", so the forbidigo pattern anchored on
// `^rand\.` never matches them — by construction, not by path exclusion.
func RollDie(r *rand.Rand) int {
	return r.Intn(6) + 1
}
