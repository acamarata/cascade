//go:build integration && seededred

// Package seeded holds the one test the red-proof CI job runs on purpose. It
// fails every time; the integration-lanes-red-assert job passes only when this
// exact failure is observed. It sits under testdata, so no ordinary build,
// vet, lint or test run ever sees it, and its tags keep it out of any lane row.
package seeded

import "testing"

// TestSeededRed fails by design (R18 M7): it proves the lane aggregation turns
// a red test into a failing job.
func TestSeededRed(t *testing.T) {
	t.Fatal("seeded red proof: this test fails on purpose")
}
