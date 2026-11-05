//go:build !windows

// Purpose: both production MCP surfaces list the installed cascade-github
// tools after an attested plugin.add on the elevated daemon registry (what
// newElevatedTestRegistry builds), and none before it: the daemon's
// daemonMCPToolRegistry and the stdio registry productionMCPDeps builds.
// A principal-bearing stdio process is withheld them.
// SPORT: cmd/cascade MCP process tools tests (P1-PLG-06).
package main

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/plugins/plugintest"
	"github.com/acamarata/cascade/internal/principal"
)

// processToolRig is an elevated daemon over a fresh store, the real
// cascade-github executable and a launch source over the daemon's store.
type processToolRig struct {
	d    elevatedDaemon
	want []string
}

func newProcessToolRig(t *testing.T) processToolRig {
	t.Helper()
	t.Cleanup(plugins.SetExecutableDirForTest(plugintest.BuildGithub(t)))
	d := newElevatedDaemon(t, filepath.Join(t.TempDir(), "data"), true)
	useTestLaunchEnv(t, d.Store)
	fp := plugins.FirstPartyManifests()[0]
	var want []string
	for _, tool := range fp.Manifest.Provides.Tools {
		want = append(want, tool.Name)
	}
	sort.Strings(want)
	return processToolRig{d: d, want: want}
}

func (r processToolRig) add(t *testing.T) {
	t.Helper()
	attestedAdd(t, r.d.Call, plugintest.GithubID, string(plugins.FirstPartyManifests()[0].Raw))
}

// githubTools is the cascade-github tools tools lists, sorted.
func githubTools(tools *mcp.ToolRegistry) []string {
	var out []string
	for _, tool := range tools.List() {
		if strings.HasPrefix(tool.Name, plugintest.GithubID+".") {
			out = append(out, tool.Name)
		}
	}
	sort.Strings(out)
	return out
}

func TestDaemonMCPToolRegistryListsProcessTools(t *testing.T) {
	r := newProcessToolRig(t)
	if got := githubTools(daemonMCPToolRegistry(r.d.Registry, grantEverything{})); len(got) != 0 {
		t.Fatalf("daemon registry listed %v before plugin.add", got)
	}
	r.add(t)
	if got := githubTools(daemonMCPToolRegistry(r.d.Registry, grantEverything{})); strings.Join(got, ",") != strings.Join(r.want, ",") {
		t.Fatalf("daemon registry listed %v, want %v", got, r.want)
	}
}

func TestStdioMCPToolRegistryListsProcessTools(t *testing.T) {
	r := newProcessToolRig(t)
	if got := githubTools(productionMCPDeps().NewTools()); len(got) != 0 {
		t.Fatalf("stdio registry listed %v before plugin.add", got)
	}
	r.add(t)
	if got := githubTools(productionMCPDeps().NewTools()); strings.Join(got, ",") != strings.Join(r.want, ",") {
		t.Fatalf("stdio registry listed %v, want %v", got, r.want)
	}
	if got := githubTools(withOwnerProcessTools(mcp.NewToolRegistry(noBuiltinTools, nil), func(k string) string {
		if k == principal.EnvToken {
			return emptyTokenMarker
		}
		return ""
	})); len(got) != 0 {
		t.Fatalf("a principal-bearing process was given %v", got)
	}
	if got := githubTools(withOwnerProcessTools(mcp.NewToolRegistry(noBuiltinTools, nil), nil)); len(got) != 0 {
		t.Fatalf("a registry with no environment reader was given %v", got)
	}
}
