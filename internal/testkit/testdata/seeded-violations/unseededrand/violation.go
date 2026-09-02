// Package unseededrand is a seeded-violation fixture, same discipline as
// ../badtime/violation.go (see its doc comment): never built by the module,
// copied into a disposable temp module at test time by lintwall_test.go to
// prove the no-unseeded-rand forbidigo rule fires against the real,
// committed .golangci.yml.
package unseededrand

import "math/rand"

// RollDie deliberately uses the global, unseeded math/rand source instead
// of an explicitly seeded *rand.Rand — the exact shape the no-unseeded-rand
// lint rule exists to forbid in non-test domain logic
// (12-QUALITY-CONSTITUTION.md Art.7.3: seeded randomness).
func RollDie() int {
	return rand.Intn(6) + 1
}

// CoinFlip deliberately calls another global-source function, proving the
// rule covers more than one entry point into math/rand's default source.
func CoinFlip() float64 {
	return rand.Float64()
}
