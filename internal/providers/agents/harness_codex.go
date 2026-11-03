// Purpose: registers the codex harness: its owning plugin, closed
// credential table and the factory that adapts a SpawnSite to codex.Config.
// Inputs: a SpawnSite built by the host shim for one spawn.
// Outputs: a codex App Server driver as provider.AgentProvider (and
// ExecutableReporter).
// Constraints: the variable names come from the codex driver's constants;
// the subscription class carries no key variable (auth.json is linked in
// CODEX_HOME); a probe-only site (no Clock) never scans.
// SPORT: internal.providers.agents/ADD (P1-AGT-05).

package agents

import (
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/agents/codex"
)

// HarnessCodex is the codex harness id.
const HarnessCodex HarnessID = "codex"

var codexRow = Binding{
	Harness:      HarnessCodex,
	PluginID:     "cascade-codex",
	Runtime:      RuntimeHarnessCLI,
	LaneID:       LanePrefix + string(HarnessCodex),
	ConfigDirVar: codex.VendorAuthVar,
	Credentials: []CredentialSpec{
		{Class: CredentialSubscription, KeyVar: ""},
		{Class: CredentialAPIKey, KeyVar: codex.KeyVarAPIKey},
	},
	Binaries: []string{"codex"},
}

var (
	_ = RegisterHarness(codexRow, newCodexDriver)

	_ provider.ExecutableReporter = (*codex.Driver)(nil)
)

// newCodexDriver adapts a SpawnSite to codex.Config.
func newCodexDriver(site SpawnSite) (provider.AgentProvider, error) {
	if err := checkSite(codexRow, site); err != nil {
		return nil, err
	}
	var match func([]byte) (string, bool)
	if site.Clock != nil {
		var err error
		if match, err = ProcessDetectorMatch(); err != nil {
			return nil, err
		}
	}
	d, err := codex.New(codex.Config{
		Binary:        site.Binary,
		Posture:       site.Posture,
		Defaults:      site.Defaults,
		RuntimeHome:   site.RuntimeHome,
		VendorAuthVar: site.VendorAuthVar,
		HookPackPath:  site.HookPackPath,
		ContentMatch:  match,
		Clock:         site.Clock,
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}
