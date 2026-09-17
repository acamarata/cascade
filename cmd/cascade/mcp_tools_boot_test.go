//go:build !windows

// Purpose: the one MCP-tool assertion that cannot run on Windows, because
//
//	the symbol it checks (bootCapabilities) lives in the POSIX-only
//	daemon composition. Split out under its own build tag rather than
//	guarded inside the portable file, so the portable file compiles
//	everywhere and this one's absence is visible.
//
// SPORT: cmd/cascade/mcp (ADD) — P1-E16-W4-S34-T2.
package main

import (
	"testing"

	"github.com/acamarata/cascade/internal/mcp/coretools"
)

// TestTheMCPToolCapabilitiesAreRegisteredAtBoot pins the join the filter
// depends on: the daemon's ONE capability registry must know the names
// the tools ask about, or every tool is withheld for a reason unrelated
// to the operator's grants and `cascade policy grant memory.write` has
// nothing to grant against.
func TestTheMCPToolCapabilitiesAreRegisteredAtBoot(t *testing.T) {
	booted := map[string]bool{}
	for _, capability := range bootCapabilities() {
		booted[capability.Name] = true
	}
	for _, spec := range coretools.Specs() {
		if !booted[spec.Capability] {
			t.Errorf("%q needs capability %q, which boot does not register", spec.Name, spec.Capability)
		}
	}
}
