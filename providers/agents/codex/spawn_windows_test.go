//go:build windows

package codex

import (
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// TestCodexSpawnRefusesOnWindows pins the tier-2 refusal: an entitled,
// clocked driver still refuses every Spawn on windows with the exact
// provider.ErrPlatform sentinel (identity, not errors.Is) and starts no child.
func TestCodexSpawnRefusesOnWindows(t *testing.T) {
	d, err := New(Config{Binary: "/usr/bin/codex", Posture: entitled(true), Defaults: testDefaults(), Clock: testClock{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Spawn(testCtx(t), provider.AgentJobSpec{}); err != provider.ErrPlatform {
		t.Fatalf("Spawn on windows = %v, want provider.ErrPlatform", err)
	}
}
