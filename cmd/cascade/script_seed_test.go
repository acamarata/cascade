package main

import (
	"context"
	"os"
	"testing"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// scriptSeedPlugin is the script directive `seed-plugin <manifest>`: it
// installs a manifest straight into the script's own plugin store, the way
// an earlier signed `plugin add` would have, so a script can exercise the
// checks that need an installed plugin without an enrolled presence key.
// It is test scaffolding: no production path writes the store this way.
func scriptSeedPlugin(t *testing.T, line int, path string) {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // a fixture path the script names
	if err != nil {
		t.Fatalf("line %d: seed-plugin: %v", line, err)
	}
	paths, err := runtime.NewDefaultPathProvider()
	if err != nil {
		t.Fatalf("line %d: seed-plugin: %v", line, err)
	}
	deps := pluginDeps{Paths: paths, Clock: runtime.NewSystemClock()}
	res, err := withPluginStore(context.Background(), deps, func(ctx context.Context, store provider.Store) (plugins.AddResult, error) {
		return plugins.AddPlugin(ctx, store, raw, "", raw, false)
	})
	if err != nil || res.Outcome != plugins.AddOutcomeInstalled {
		t.Fatalf("line %d: seed-plugin %s = %v (outcome %d)", line, path, err, res.Outcome)
	}
}
