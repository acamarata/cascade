// Package farm provides local model lifecycle utilities.
package farm

import (
	"testing"
	"time"
)

// TestKeepWarmTypeExists verifies that the KeepWarm type and ShouldUnload
// function exist with the expected signatures.
func TestKeepWarmTypeExists(t *testing.T) {
	policy := KeepWarm{IdleMinutes: 10}
	if policy.IdleMinutes != 10 {
		t.Fatalf("expected IdleMinutes 10, got %d", policy.IdleMinutes)
	}

	lastUse := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := lastUse.Add(5 * time.Minute)

	// Should not unload when within idle window.
	if ShouldUnload(lastUse, now, policy) {
		t.Fatal("expected ShouldUnload to return false within idle window")
	}

	// Should unload when past idle window.
	now = lastUse.Add(11 * time.Minute)
	if !ShouldUnload(lastUse, now, policy) {
		t.Fatal("expected ShouldUnload to return true past idle window")
	}
}
