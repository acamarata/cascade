package agents

// Purpose: bind the native run driver to its owning harness plugin.
// Inputs: the host custody site's profile and credential selection.
// Outputs: an executable-reporting AgentProvider. Constraints: closed key row.

import (
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/agents/opencode"
)

// HarnessOpenCode is the OpenCode native run harness identifier.
const HarnessOpenCode HarnessID = "opencode"

var opencodeRow = Binding{
	Harness: HarnessOpenCode, PluginID: "cascade-opencode", Runtime: RuntimeHarnessCLI,
	LaneID: "agent:opencode", ConfigDirVar: opencode.VendorAuthVar,
	Credentials: []CredentialSpec{{Class: CredentialCompatKey, KeyVar: opencode.KeyVarCompatKey}},
	Binaries:    []string{"opencode"},
}

var _ = RegisterHarness(opencodeRow, newOpenCodeDriver)

func newOpenCodeDriver(site SpawnSite) (provider.AgentProvider, error) {
	if err := checkSite(opencodeRow, site); err != nil {
		return nil, err
	}
	var match func([]byte) (string, bool)
	if site.Clock != nil {
		var err error
		match, err = ProcessDetectorMatch()
		if err != nil {
			return nil, err
		}
	}
	return opencode.New(opencode.Config{Binary: site.Binary, Posture: site.Posture, Defaults: site.Defaults, RuntimeHome: site.RuntimeHome, VendorAuthVar: site.VendorAuthVar, Clock: site.Clock, ContentMatch: match})
}
