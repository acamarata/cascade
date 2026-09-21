// Purpose: CLI-facing tests for `cascade plugin search` (plugin_search.go)
// — table match/browse/truncation, the --json envelope, and the daemon-
// down error path. Catalog decision logic (pluginCatalogSearch's
// fail-closed contract) and the real RPC-dispatch proof live in
// plugin_search_catalog_test.go (D8 — split to stay under Art.10.3's
// 300-line cap).
//
// LAYERING: the no-network-unit-lane gate (Art.7.2) bans "net"/"net/http"
// in any non-integration _test.go file. CLI tests inject
// pluginDeps.SearchCall (no transport). TestPluginSearchDaemonDown dials
// an unlistened socket over a REAL DialContext (client.UnixDialer,
// status_test.go's idiom) with an explicit Embedded=false DaemonlessState
// — a real connection failure, not a transport this file imports.
//
// SPORT: cli/plugin-search/ADD (P1-E24-W5-S50-T2).
package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

func testSearchEntries() []pluginSearchEntry {
	return []pluginSearchEntry{
		{RegistryIndexEntry: plugin.RegistryIndexEntry{ID: "fmt-tool", Name: "formatter", Description: "Formats source files on save", LatestVersion: "1.2.0"}},
		{RegistryIndexEntry: plugin.RegistryIndexEntry{ID: "lint-tool", Name: "linter", Description: "Lints source files", LatestVersion: "0.9.0"}},
	}
}

// pluginSearchCase is one TestPluginSearch row.
type pluginSearchCase struct {
	name    string
	args    []string
	call    func(context.Context, string) ([]pluginSearchEntry, error)
	wantOut []string // substrings stdout must contain
}

func pluginSearchCases(t *testing.T) []pluginSearchCase {
	return []pluginSearchCase{
		{
			name: "query match",
			args: []string{"search", "formatter"},
			call: func(_ context.Context, q string) ([]pluginSearchEntry, error) {
				if q != "formatter" {
					t.Fatalf("query passed to Search = %q, want verbatim %q", q, "formatter")
				}
				return []pluginSearchEntry{testSearchEntries()[0]}, nil
			},
			wantOut: []string{"formatter", "1.2.0"},
		},
		{ // header only: an empty table, not an error.
			name:    "no match",
			args:    []string{"search", "nonexistent-plugin-xyz"},
			call:    func(context.Context, string) ([]pluginSearchEntry, error) { return nil, nil },
			wantOut: []string{"NAME", "VERSION"},
		},
		{
			name: "browse (no arg)",
			args: []string{"search"},
			call: func(_ context.Context, q string) ([]pluginSearchEntry, error) {
				if q != "" {
					t.Fatalf("browse mode must pass q=\"\", got %q", q)
				}
				return testSearchEntries(), nil
			},
			wantOut: []string{"formatter", "linter"},
		},
		{ // 120-char description must not appear whole: 60-char cutoff.
			name: "description truncated at 60",
			args: []string{"search"},
			call: func(context.Context, string) ([]pluginSearchEntry, error) {
				return []pluginSearchEntry{{RegistryIndexEntry: plugin.RegistryIndexEntry{Name: "p", Description: strings.Repeat("x", 120), LatestVersion: "1.0.0"}}}, nil
			},
			wantOut: []string{strings.Repeat("x", 60)},
		},
		{ // D5: a builtin-fallback row's Runtime survives to the column.
			name: "runtime column shows a known runtime",
			args: []string{"search"},
			call: func(context.Context, string) ([]pluginSearchEntry, error) {
				return []pluginSearchEntry{{RegistryIndexEntry: plugin.RegistryIndexEntry{Name: "p", LatestVersion: "1.0.0"}, Runtime: "go"}}, nil
			},
			wantOut: []string{"go"},
		},
	}
}

func TestPluginSearch(t *testing.T) {
	for _, tc := range pluginSearchCases(t) {
		t.Run(tc.name, func(t *testing.T) { runPluginSearchCase(t, tc) })
	}
}

func runPluginSearchCase(t *testing.T, tc pluginSearchCase) {
	t.Helper()
	deps := testPluginDeps(t)
	deps.SearchCall = tc.call
	stdout, stderr, err := runPluginCLI(t, deps, tc.args...)
	if err != nil {
		t.Fatalf("plugin %v: unexpected error: %v (stderr=%s)", tc.args, err, stderr)
	}
	for _, want := range tc.wantOut {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	}
	if tc.name == "description truncated at 60" && strings.Contains(stdout, strings.Repeat("x", 120)) {
		t.Errorf("stdout contains the full 120-char description; want it truncated:\n%s", stdout)
	}
}

// TestPluginSearchJSON: --json's envelope "data" field is the entries
// array, decodable as plain plugin.RegistryIndexEntry (D5's embedding
// never breaks that contract) AND carrying "runtime" when known.
func TestPluginSearchJSON(t *testing.T) {
	deps := testPluginDeps(t)
	deps.SearchCall = func(context.Context, string) ([]pluginSearchEntry, error) {
		return []pluginSearchEntry{
			{RegistryIndexEntry: testSearchEntries()[0].RegistryIndexEntry},
			{RegistryIndexEntry: testSearchEntries()[1].RegistryIndexEntry, Runtime: "node"},
		}, nil
	}
	stdout, stderr, err := runPluginCLI(t, deps, "search", "--json")
	if err != nil {
		t.Fatalf("unexpected error: %v (stderr=%s)", err, stderr)
	}
	var envelope struct {
		OK   bool                        `json:"ok"`
		Data []plugin.RegistryIndexEntry `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}
	if !envelope.OK {
		t.Fatalf("envelope.OK = false, want true")
	}
	if len(envelope.Data) != 2 || envelope.Data[0].Name != "formatter" || envelope.Data[1].Name != "linter" {
		t.Fatalf("envelope.Data = %+v, want the two testSearchEntries() rows in order", envelope.Data)
	}
	var withRuntime struct {
		Data []struct {
			Name    string `json:"name"`
			Runtime string `json:"runtime"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &withRuntime); err != nil {
		t.Fatalf("stdout is not valid JSON (runtime decode): %v", err)
	}
	if withRuntime.Data[1].Runtime != "node" {
		t.Fatalf("data[1].runtime = %q, want %q", withRuntime.Data[1].Runtime, "node")
	}
}

// TestPluginSearchJSONNoMatchEmptyArray: no-match renders as an empty
// array, never JSON null. Split from TestPluginSearchJSON (funlen's
// 50-line cap).
func TestPluginSearchJSONNoMatchEmptyArray(t *testing.T) {
	deps := testPluginDeps(t)
	deps.SearchCall = func(context.Context, string) ([]pluginSearchEntry, error) { return nil, nil }
	stdout, _, err := runPluginCLI(t, deps, "search", "nothing-matches", "--json")
	if err != nil {
		t.Fatalf("no-match: unexpected error: %v", err)
	}
	var empty struct {
		Data []plugin.RegistryIndexEntry `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &empty); err != nil {
		t.Fatalf("no-match stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}
	if empty.Data == nil {
		t.Fatalf("no-match \"data\" decoded as null (nil slice), want an empty array: %s", stdout)
	}
	if len(empty.Data) != 0 {
		t.Fatalf("no-match \"data\" = %+v, want empty", empty.Data)
	}
}

// TestPluginSearchDaemonDown: a real connection failure, non-zero exit
// (D9, s50t2-t0-decisions-2.txt) — a daemon IS configured for this
// process (an explicit [daemon].socket entry written into config.toml,
// which pluginDaemonConfigured treats as a real, operator-stated setup)
// but nothing is listening, so pluginSearchCaller dials and surfaces the
// failure rather than answering from the local catalog. That fallback is
// legitimate ONLY when no daemon is configured at all — see
// plugin_search_catalog_test.go's TestPluginSearchEmbeddedFallback for
// that branch. Uses the plain runPluginCLI helper (Embedded: true in
// ctx, i.e. no live daemon was probed either) on purpose: it proves the
// branch decision no longer depends on the probe's dial outcome, the
// exact defect the confirming review's PRE-FLAG 3 found (a previous
// version of this test forced Embedded:false, which the review called "a
// probe-succeeded/dial-failed race, not 'daemon not running'").
func TestPluginSearchDaemonDown(t *testing.T) {
	deps := testPluginDeps(t)
	if err := os.WriteFile(deps.Paths.ConfigPath(), []byte("[daemon]\nsocket = \"/nonexistent/daemon.sock\"\n"), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	deps.DialContext = productionPluginDeps().DialContext
	stdout, stderr, err := runPluginCLI(t, deps, "search", "anything")
	if err == nil {
		t.Fatalf("expected an error dialing a socket nothing listens on; stdout=%q", stdout)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err = %v, want KindUnavailable", err)
	}
	if stderr == "" && err.Error() == "" {
		t.Fatalf("expected a readable error message, got none")
	}
}
