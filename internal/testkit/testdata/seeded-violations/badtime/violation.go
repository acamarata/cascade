// Package badtime is a seeded-violation fixture (never built as part of the
// module — it lives under testdata/, which both `go build ./...` and
// golangci-lint's default package discovery skip entirely). It exists so
// lintwall_test.go can copy its source into a disposable temp module at
// test time and run the repo's real, committed .golangci.yml against it,
// proving the no-bare-time.Now forbidigo rule actually fires.
//
// 12-QUALITY-CONSTITUTION.md Art.1.1/Art.7.1: this is fixture data, not
// shipped or even compiled code.
package badtime

import "time"

// LastSeen deliberately reads the wall clock directly instead of taking an
// injected Clock — the exact shape the no-bare-time.Now lint rule exists to
// forbid in non-test domain logic (02-TARGET-STRUCTURE.md §v1.1).
func LastSeen() time.Time {
	return time.Now()
}

// Elapsed deliberately calls time.Since directly, the rule's other half.
func Elapsed(start time.Time) time.Duration {
	return time.Since(start)
}
