// Purpose: registers the antigravity harness: its owning plugin, credential
// table and the factory that adapts a SpawnSite to antigravity.Config.
// Inputs: a SpawnSite built by the host shim for one spawn.
// Outputs: an antigravity print-mode driver as provider.AgentProvider (and
// ExecutableReporter).
// Constraints: no auth source is recorded for agy (fixture-only), so the
// table holds the subscription class alone; a probe-only site (no Clock)
// never scans.
// SPORT: internal.providers.agents/ADD (P1-AGT-07g).

package agents

import (
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/agents/antigravity"
)

// HarnessAntigravity is the antigravity harness id.
const HarnessAntigravity HarnessID = "antigravity"

var antigravityRow = Binding{
	Harness:      HarnessAntigravity,
	PluginID:     "antigravity",
	Runtime:      RuntimeHarnessCLI,
	LaneID:       LanePrefix + string(HarnessAntigravity),
	ConfigDirVar: "ANTIGRAVITY_HOME",
	Credentials:  []CredentialSpec{{Class: CredentialSubscription, KeyVar: ""}},
	Binaries:     []string{"agy"},
}

var _ = RegisterHarness(antigravityRow, newAntigravityDriver)

// newAntigravityDriver adapts a SpawnSite to antigravity.Config.
func newAntigravityDriver(site SpawnSite) (provider.AgentProvider, error) {
	if err := checkSite(antigravityRow, site); err != nil {
		return nil, err
	}
	var match func([]byte) (string, bool)
	if site.Clock != nil {
		var err error
		if match, err = ProcessDetectorMatch(); err != nil {
			return nil, err
		}
	}
	d, err := antigravity.New(antigravity.Config{
		Binary:       site.Binary,
		Posture:      site.Posture,
		Defaults:     site.Defaults,
		RuntimeHome:  site.RuntimeHome,
		Clock:        site.Clock,
		ContentMatch: match,
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}
