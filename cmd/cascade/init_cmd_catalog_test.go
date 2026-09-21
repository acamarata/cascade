package main

// Purpose: TestInitPluginCatalogMapsRegistryRows, split out of
//   init_cmd_test.go purely to keep that file under Art.10.3's 300-line
//   cap (this ticket's own addition pushed it to 304 — the same
//   established remedy config_widget.go/config_sections.go's own header
//   comments record for the identical reason).
// SPORT: cli/plugin-search/ADD (P1-E24-W5-S50-T2, D12,
//   s50t2-t0-decisions-2.txt).

import (
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
)

// TestInitPluginCatalogMapsRegistryRows is D12 (s50t2-t0-decisions-2.txt):
// TestTheCatalogComesFromTheLiveRegistry (init_cmd_test.go) only ever runs
// the UNCONFIGURED-registry state through Entries() (no config.toml, no
// [registry] section, so pluginCatalogSearch never returns a
// fromRegistry=true row). This test drives catalogEntriesFromRows
// (init_adapters.go) — Entries()'s own mapping step — directly with
// synthetic rows standing in for the VERIFIED-registry state, proving the
// wizard's Name/Description/DefaultOn conversion (not just
// pluginCatalogSearch's own fail-closed contract, already covered by
// plugin_search_catalog_test.go's TestPluginCatalogSearchVerified) treats
// a registry row as discovered-and-opt-in (DefaultOn=false, R-14.93) and a
// builtin row as always-on (DefaultOn=true) — without a live or fake HTTP
// registry fetch, which this package's untagged unit lane (Art.7.2)
// forbids.
func TestInitPluginCatalogMapsRegistryRows(t *testing.T) {
	rows := []pluginSearchEntry{
		{RegistryIndexEntry: plugin.RegistryIndexEntry{Name: "zzz-builtin", Description: "ships in this binary"}},
		{RegistryIndexEntry: plugin.RegistryIndexEntry{Name: "formatter", Description: "formats source files on save"}, fromRegistry: true},
	}
	entries := catalogEntriesFromRows(rows)
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	// Sorted by Name: "formatter" < "zzz-builtin".
	registryRow, builtinRow := entries[0], entries[1]
	if registryRow.Name != "formatter" || registryRow.Description != "formats source files on save" {
		t.Errorf("registry row = %+v, want Name=formatter carrying its own description", registryRow)
	}
	if registryRow.DefaultOn {
		t.Error("registry-sourced row has DefaultOn=true, want false (R-14.93: never pre-checked)")
	}
	if builtinRow.Name != "zzz-builtin" || builtinRow.Description != "ships in this binary" {
		t.Errorf("builtin row = %+v, want Name=zzz-builtin carrying its own description", builtinRow)
	}
	if !builtinRow.DefaultOn {
		t.Error("builtin row has DefaultOn=false, want true (always compiled in and active)")
	}
}
