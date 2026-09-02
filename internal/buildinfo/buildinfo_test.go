// Purpose: unit tests for the §D-33 install-channel resolution logic and the
//
//	documented dev-build defaults.
//
// Inputs:  none (pure in-memory state; package vars are saved/restored per
//
//	test so cases never leak into each other or into other test files).
//
// Outputs: pass/fail.
// Constraints: no network, no filesystem, no sleeps (12-QUALITY-CONSTITUTION
//
//	Art.7). Mutates package-level vars under test, so this file must not
//	run in parallel with itself (no t.Parallel()).
//
// SPORT: internal/buildinfo — ldflags stamp source of truth (A-T6).
package buildinfo

import "testing"

func TestResolvedInstallChannel_ValidValues(t *testing.T) {
	t.Cleanup(func() { InstallChannel = "" })

	for channel := range ValidInstallChannels {
		InstallChannel = channel
		if got := ResolvedInstallChannel(); got != channel {
			t.Errorf("ResolvedInstallChannel() with InstallChannel=%q = %q, want %q", channel, got, channel)
		}
	}
}

func TestResolvedInstallChannel_UnstampedFallsBackToManual(t *testing.T) {
	t.Cleanup(func() { InstallChannel = "" })
	InstallChannel = ""

	if got := ResolvedInstallChannel(); got != "manual" {
		t.Errorf("ResolvedInstallChannel() with unstamped InstallChannel = %q, want %q", got, "manual")
	}
}

// TestResolvedInstallChannel_UnknownValueFallsBackToManual is the error-path
// case: a corrupt or foreign ldflags stamp (e.g. a typo'd channel from a
// future install-tooling ticket) must never surface as a bogus channel name
// — it must resolve to the safe "manual" default rather than propagate an
// unrecognized value to callers.
func TestResolvedInstallChannel_UnknownValueFallsBackToManual(t *testing.T) {
	t.Cleanup(func() { InstallChannel = "" })

	for _, bogus := range []string{"homebrew", "SCRIPT", " script", "script "} {
		InstallChannel = bogus
		if got := ResolvedInstallChannel(); got != "manual" {
			t.Errorf("ResolvedInstallChannel() with InstallChannel=%q = %q, want %q", bogus, got, "manual")
		}
	}
}

func TestDevDefaults(t *testing.T) {
	// This test does not stamp any var — it documents the unstamped
	// (`go build` with no -ldflags) defaults a dev build must print, which
	// cmd/cascade/version.go mirrors exactly (R-14.108).
	if Version != "dev" {
		t.Errorf("Version default = %q, want %q", Version, "dev")
	}
	if Commit != "none" {
		t.Errorf("Commit default = %q, want %q", Commit, "none")
	}
	if Date != "unknown" {
		t.Errorf("Date default = %q, want %q", Date, "unknown")
	}
	if ResolvedInstallChannel() != "manual" {
		t.Errorf("ResolvedInstallChannel() default = %q, want %q", ResolvedInstallChannel(), "manual")
	}
}
