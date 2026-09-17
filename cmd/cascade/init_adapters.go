package main

// Purpose: the adapters `cascade init` hands the wizard
//   (P1-E16-W4-S35-T6). Each one is a thin call onto the shipped
//   implementation of that step: the S-34/S-35.T1 harness adapters, the
//   builtin plugin registry, the S-07.T2 service installer, the S-07.T6
//   helper enrollment, doctor's first-run lane, and the cascade binary
//   itself for the two hand-offs.
// Constraints: Art.1 — no adapter here does a step's work itself. An
//   adapter that could not reach its implementation returns that
//   implementation's error; none of them substitutes a success.
// SPORT: cmd/cascade init adapters (ADD) — P1-E16-W4-S35-T6.

import (
	"context"
	"os"
	"sort"

	"github.com/acamarata/cascade/internal/plugins"
	cascadeinit "github.com/acamarata/cascade/internal/runtime/init"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/claude"
	"github.com/acamarata/cascade/plugins/codex"
	"github.com/acamarata/cascade/plugins/opencode"
)

// initGetenv is the environment accessor the detector reads. A named
// function rather than os.Getenv inline, so every seam in this file names
// where its data comes from.
func initGetenv(key string) string { return os.Getenv(key) }

// initPathExists is the detector's production PathProbe: one stat, no
// read. Named here rather than reused from another package so the two
// composition roots that build a detector cannot drift into probing
// differently.
func initPathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// initCwd resolves the project directory harness instructions are written
// for.
func initCwd() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "cascade init: resolve the working directory")
	}
	return cwd, nil
}

// initHarnessWirer installs one harness's instruction files, hook pack
// and MCP entry by calling that harness's own adapter.
type initHarnessWirer struct{}

var _ cascadeinit.HarnessWirer = initHarnessWirer{}

// Wire dispatches to the shipped adapter for kind.
//
// A kind with no adapter is a REFUSAL, not a skip: the wizard only asks
// about harnesses the detector reported, so reaching this branch means
// the detector and the adapters disagree about which harnesses exist —
// and silently wiring nothing would report a harness set up that is not.
func (initHarnessWirer) Wire(ctx context.Context, kind, cwd string) error {
	switch kind {
	case "claude":
		// InstallAll, not Install: Install writes the instruction files
		// and nothing else, and step 6's own plan line promises
		// "instruction files, hook pack, MCP entry". Calling the narrow
		// one made `cascade init` print "claude wired" for a harness
		// whose `cascade context harness list` still reported
		// cascade_registered=false.
		_, err := claude.InstallAll(ctx, cwd)
		return err
	case "codex":
		_, err := codex.Install(ctx, cwd)
		return err
	case "opencode":
		_, err := opencode.Install(ctx, cwd)
		return err
	default:
		return cascade.Newf(cascade.KindInternal,
			"cascade init: the detector reported harness %q, which has no install adapter", kind)
	}
}

// initPluginCatalog renders step 4's checklist from the LIVE builtin
// registry.
//
// Not from a list written here: a checklist offering a plugin this build
// does not register would install nothing and report success, which is
// the Article-1 failure this surface is most exposed to. A build that
// grows a plugin gets a catalog row for free; one that loses a plugin
// loses the row.
type initPluginCatalog struct{}

var _ cascadeinit.Catalog = initPluginCatalog{}

// Entries lists every registered builtin plugin.
func (initPluginCatalog) Entries() []cascadeinit.CatalogEntry {
	reg := &plugins.BuiltinRegistry{}
	if err := reg.Load(); err != nil {
		// A registry that will not load contributes no rows rather than
		// invented ones. The wizard states an empty catalog plainly, and
		// `cascade plugin list` is where a load failure is diagnosed.
		return nil
	}
	registered := reg.List()
	out := make([]cascadeinit.CatalogEntry, 0, len(registered))
	for _, r := range registered {
		// The checkbox is keyed on the manifest ID, because that is what
		// every other surface names a plugin by; the display name is the
		// row's description. A cascade.plugin/v2 manifest carries no
		// top-level description field, so the row says the plugin's own
		// name rather than a sentence invented here.
		out = append(out, cascadeinit.CatalogEntry{
			Name:        r.Manifest.ID,
			Description: r.Manifest.Name,
			DefaultOn:   true,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
