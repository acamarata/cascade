// Purpose: registers the claude harness: its owning plugin, closed
// credential table and the factory that adapts a SpawnSite to claude.Config.
// Inputs: a SpawnSite built by the host shim for one spawn.
// Outputs: a claude driver as provider.AgentProvider (and ExecutableReporter).
// Constraints: the variable names come from the claude driver's own
// constants (provenance: testdata/credentials.md); the profile and lease are
// fixed from the site, so the driver cannot be pointed at another tree.
// SPORT: internal.providers.agents/ADD (P1-AGT-02).

package agents

import (
	"context"
	"path/filepath"
	"slices"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/agents/claude"
)

// HarnessClaude is the claude harness id.
const HarnessClaude HarnessID = "claude"

var claudeRow = Binding{
	Harness:      HarnessClaude,
	PluginID:     "cascade-claude",
	Runtime:      RuntimeHarnessCLI,
	LaneID:       LanePrefix + string(HarnessClaude),
	ConfigDirVar: claude.VendorConfigDirVar,
	Credentials: []CredentialSpec{
		{Class: CredentialOAuthToken, KeyVar: claude.KeyVarOAuthToken},
		{Class: CredentialAPIKey, KeyVar: claude.KeyVarAPIKey},
	},
	Binaries: []string{"claude"},
}

var (
	_ = RegisterHarness(claudeRow, newClaudeDriver)

	_ provider.ExecutableReporter = (*claude.Driver)(nil)
)

// checkSite refuses a site naming a variable or class outside the binding's
// closed credential table (B-1). Empty fields pass: a probe-only site has none.
func checkSite(b Binding, site SpawnSite) error {
	if site.VendorAuthVar != "" && site.VendorAuthVar != b.ConfigDirVar {
		return cascade.Newf(cascade.KindInvalidInput, "agents: %s: vendor auth variable %q is not the config directory variable", b.Harness, site.VendorAuthVar)
	}
	if site.KeyVar == "" && site.CredentialClass == "" {
		return nil
	}
	if !slices.ContainsFunc(b.Credentials, func(c CredentialSpec) bool { return c.Class == site.CredentialClass && c.KeyVar == site.KeyVar }) {
		return cascade.Newf(cascade.KindInvalidInput, "agents: %s: credential class %q with variable %q is not in the binding's table", b.Harness, site.CredentialClass, site.KeyVar)
	}
	return nil
}

// newClaudeDriver adapts a SpawnSite to claude.Config. A site without a Clock
// builds a probe-only driver: Capabilities works, Spawn refuses.
func newClaudeDriver(site SpawnSite) (provider.AgentProvider, error) {
	if err := checkSite(claudeRow, site); err != nil {
		return nil, err
	}
	prof := claude.Profile{
		VendorAuthVar:   site.VendorAuthVar,
		KeyVar:          site.KeyVar,
		RuntimeHome:     site.RuntimeHome,
		Posture:         site.Posture,
		CredentialClass: claude.CredentialClass(site.CredentialClass),
	}
	d, err := claude.New(claude.Config{
		Binary:       site.Binary,
		HookPackPath: site.HookPackPath,
		Profile:      siteProfile{prof},
		Worktree:     siteLease{root: site.WorktreeRoot},
		Defaults:     site.Defaults,
		Clock:        site.Clock,
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

// siteProfile resolves to the one profile the site carries.
type siteProfile struct{ prof claude.Profile }

func (p siteProfile) Resolve(context.Context) (claude.Profile, error) { return p.prof, nil }

// siteLease leases the site's worktree root, whatever the scope.
type siteLease struct{ root string }

func (l siteLease) Lease(context.Context, provider.WorktreeScope) (claude.Lease, error) {
	return claude.Lease{Root: l.root, GitDir: filepath.Join(l.root, ".git")}, nil
}
