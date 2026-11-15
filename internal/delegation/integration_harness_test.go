//go:build integration && !windows

// Purpose: stands the test-only socket driver in for the registered harness.

package delegation

import (
	"sync"

	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/pkg/provider"
)

// swapOnce makes the socket driver stand in once per test binary.
var swapOnce sync.Once

// useSocketHarness swaps the test-only socket driver in for the registered
// harness of the same id, once per test binary (production registers every
// harness of the closed set at init, so the test cannot register its own).
func useSocketHarness() {
	swapOnce.Do(func() {
		_ = agents.SwapHarnessForTest(agents.Binding{Harness: socketHarness, PluginID: socketHarness,
			Runtime: agents.RuntimeHarnessCLI, LaneID: agents.LanePrefix + socketHarness, ConfigDirVar: "TEST_AGENT_CONFIG_DIR",
			Credentials: []agents.CredentialSpec{{Class: agents.CredentialSubscription}}, Binaries: []string{socketBinary}},
			func(agents.SpawnSite) (provider.AgentProvider, error) { return socketDriver{}, nil })
	})
}
