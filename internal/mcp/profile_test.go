package mcp_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/pkg/plugin"
)

// TestToolProfileParseFailsClosed pins the parse rule: only the exact
// lowercase names parse to themselves; every other string is compact.
func TestToolProfileParseFailsClosed(t *testing.T) {
	narrow := []string{"", "FULL", " full", "full ", "Compact-Write", "compact_write", "admin", "compact"}
	for _, in := range narrow {
		if got := mcp.ParseToolProfile(in); got != mcp.ProfileCompact {
			t.Errorf("ParseToolProfile(%q) = %q, want %q", in, got, mcp.ProfileCompact)
		}
	}
	exact := map[string]mcp.ToolProfile{
		"compact-write": mcp.ProfileCompactWrite,
		"full":          mcp.ProfileFull,
	}
	for in, want := range exact {
		if got := mcp.ParseToolProfile(in); got != want {
			t.Errorf("ParseToolProfile(%q) = %q, want %q", in, got, want)
		}
	}
	if got := mcp.ProfileMembers(mcp.ProfileFull); got != nil {
		t.Errorf("ProfileMembers(full) = %v, want nil", got)
	}
	var zero mcp.ToolProfile
	if got := mcp.ProfileMembers(zero); !slices.Equal(got, mcp.CompactToolNames) {
		t.Errorf("ProfileMembers(zero value) = %v, want the compact set %v", got, mcp.CompactToolNames)
	}
	if got := mcp.ProfileMembers(mcp.ToolProfile("FULL")); !slices.Equal(got, mcp.CompactToolNames) {
		t.Errorf("ProfileMembers(\"FULL\") = %v, want the compact set", got)
	}
}

// readGrantPluginTools are the plugin tools the compact set may hold.
// internal/** may not import plugins/**, so the read grant is checked
// through pkg/plugin.Builtins when the plugin is registered in this test
// binary; plugins/pbd's own tests prove its tool passes the read-only
// grant filter.
var readGrantPluginTools = map[string]bool{
	"cascade_plugin_pbd_status": true,
}

// TestCompactProfileIsReadOnly fails if any compact member could mutate.
func TestCompactProfileIsReadOnly(t *testing.T) {
	specs := map[string]coretools.Spec{}
	for _, s := range coretools.Specs() {
		specs[s.Name] = s
	}
	grants := pluginToolGrants()
	for _, name := range mcp.CompactToolNames {
		if s, ok := specs[name]; ok {
			if s.Mutating {
				t.Errorf("compact member %q is a mutating coretools Spec", name)
			}
			continue
		}
		if !readGrantPluginTools[name] || !strings.HasPrefix(name, "cascade_plugin_") {
			t.Errorf("compact member %q is neither a coretools Spec nor a read-grant plugin tool", name)
			continue
		}
		if g, ok := grants[name]; ok && !slices.Equal(g, []string{"read"}) {
			t.Errorf("compact member %q is a plugin tool with grants %v, want exactly [read]", name, g)
		}
	}
	for alias, target := range mcp.CompactAlias {
		if !slices.Contains(mcp.CompactToolNames, target) {
			t.Errorf("compact alias %q names %q, which is not a compact member", alias, target)
		}
	}
}

// pluginToolGrants maps each registered builtin plugin tool to its grants.
func pluginToolGrants() map[string][]string {
	out := map[string][]string{}
	for _, r := range plugin.Builtins() {
		for _, tool := range r.Manifest.Provides.Tools {
			out[tool.Name] = r.Grants
		}
	}
	return out
}

// TestProfileSetsAdditive proves a producer adds a member by appending to
// a set and nothing else. Counts are derived from the sets themselves.
func TestProfileSetsAdditive(t *testing.T) {
	const scratch = "cascade_scratch_profile_probe"
	origCompact, origWrite := mcp.CompactToolNames, mcp.CompactWriteToolNames
	t.Cleanup(func() { mcp.CompactToolNames, mcp.CompactWriteToolNames = origCompact, origWrite })

	assertMembers(t, mcp.ProfileCompact, len(origCompact), false)
	assertMembers(t, mcp.ProfileCompactWrite, len(origCompact)+len(origWrite), false)

	mcp.CompactToolNames = append(slices.Clone(origCompact), scratch)
	assertMembers(t, mcp.ProfileCompact, len(origCompact)+1, true)
	assertMembers(t, mcp.ProfileCompactWrite, len(origCompact)+len(origWrite)+1, true)
	mcp.CompactToolNames = origCompact

	mcp.CompactWriteToolNames = append(slices.Clone(origWrite), scratch)
	assertMembers(t, mcp.ProfileCompact, len(origCompact), false)
	assertMembers(t, mcp.ProfileCompactWrite, len(origCompact)+len(origWrite)+1, true)
	if got := mcp.ProfileMembers(mcp.ProfileFull); got != nil {
		t.Errorf("ProfileMembers(full) = %v after an append, want nil", got)
	}
}

// assertMembers checks the member count of p and whether the scratch probe
// is among them.
func assertMembers(t *testing.T, p mcp.ToolProfile, want int, hasScratch bool) {
	t.Helper()
	got := mcp.ProfileMembers(p)
	if len(got) != want {
		t.Errorf("ProfileMembers(%q) has %d members, want %d: %v", p, len(got), want, got)
	}
	if slices.Contains(got, "cascade_scratch_profile_probe") != hasScratch {
		t.Errorf("ProfileMembers(%q) scratch presence = %v, want %v", p, !hasScratch, hasScratch)
	}
}
