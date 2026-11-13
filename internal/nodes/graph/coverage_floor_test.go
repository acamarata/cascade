package graph

import "testing"

// TestCoverageAtFloorPasses verifies that a current coverage value exactly at
// the fixed floor succeeds even when the live maximum is lower.
func TestCoverageAtFloorPasses(t *testing.T) {
	if err := CheckCoverageFloor(85.0, 80.0); err != nil {
		t.Fatalf("CheckCoverageFloor(85.0, 80.0) = %v, want nil", err)
	}
}

// TestCoverageBelowFloorFails verifies that a current coverage value below the
// fixed floor fails even when the live maximum is lower.
func TestCoverageBelowFloorFails(t *testing.T) {
	if err := CheckCoverageFloor(84.9, 80.0); err == nil {
		t.Fatal("CheckCoverageFloor(84.9, 80.0) = nil, want error")
	}
}

// TestCoverageFloorUsesLiveMax verifies that the live maximum raises the
// effective floor above the fixed minimum when it is larger.
func TestCoverageFloorUsesLiveMax(t *testing.T) {
	if err := CheckCoverageFloor(86.0, 90.0); err == nil {
		t.Fatal("CheckCoverageFloor(86.0, 90.0) = nil, want error")
	}
}
