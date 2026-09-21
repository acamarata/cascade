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

	"github.com/acamarata/cascade/internal/runtime"
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
// registry, plus — when a verified registry index is configured
// (R-14.93, P1-E24-W5-S50-T2) — registry-sourced rows alongside it,
// through pluginCatalogSearch, the SAME function `plugin search`'s daemon
// RPC handler calls (plugin_search.go, D2). Neither surface re-implements
// the fail-closed decision.
//
// Not from a list written here: a checklist offering a plugin this build
// does not register would install nothing and report success, which is
// the Article-1 failure this surface is most exposed to. A build that
// grows a plugin gets a catalog row for free; one that loses a plugin
// loses the row.
type initPluginCatalog struct {
	paths runtime.PathProvider
	clock runtime.Clock
}

var _ cascadeinit.Catalog = initPluginCatalog{}

// Entries lists every registered builtin plugin, plus any verified
// registry entries (R-14.93). A registry row is never pre-checked
// (DefaultOn=false): it is a discovered, opt-in offering, unlike a
// builtin, which is always compiled in and always active.
func (c initPluginCatalog) Entries() []cascadeinit.CatalogEntry {
	ctx := context.Background()
	client, _ := buildPluginRegistryClient(ctx, c.paths, c.clock) // a warning here is redundant with registry_pubkey's doctor finding
	rows, err := pluginCatalogSearch(ctx, client, "")
	if err != nil {
		// Neither population contributes rows rather than invented ones
		// — same discipline the old builtin-only load-failure path used.
		return nil
	}
	return catalogEntriesFromRows(rows)
}

// catalogEntriesFromRows maps pluginCatalogSearch's rows onto the
// wizard's CatalogEntry shape (D12, s50t2-t0-decisions-2.txt): a
// registry-sourced row (fromRegistry=true) renders DefaultOn=false — a
// discovered, opt-in offering, per R-14.93 — while a builtin row
// (fromRegistry=false, always compiled in and always active) renders
// DefaultOn=true. Split out of Entries() so this mapping is directly
// testable against synthetic rows standing in for the verified-registry
// state, without a live or fake HTTP registry fetch (Art.7.2's
// no-network unit lane forbids one in this file's test) —
// init_cmd_test.go's TestInitPluginCatalogMapsRegistryRows exercises it
// directly; TestTheCatalogComesFromTheLiveRegistry (same file) still
// proves Entries() itself, but only ever ran the unconfigured-registry
// state through it.
func catalogEntriesFromRows(rows []pluginSearchEntry) []cascadeinit.CatalogEntry {
	out := make([]cascadeinit.CatalogEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, cascadeinit.CatalogEntry{
			Name:        r.Name,
			Description: r.Description,
			DefaultOn:   !r.fromRegistry,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
