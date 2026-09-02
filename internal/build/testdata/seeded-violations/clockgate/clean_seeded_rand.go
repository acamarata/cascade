// Package violation (clean_seeded_rand.go) is a false-positive probe: a
// properly seeded *rand.Rand VALUE's Intn call must never trip the gate —
// its selector base is a local variable identifier, never the "rand"
// package identifier, so it is not even a candidate for alias resolution.
package violation

import "math/rand"

func SeededDraw(seed int64) int {
	r := rand.New(rand.NewSource(seed))
	return r.Intn(10)
}
