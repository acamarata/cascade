package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// Purpose (this file): the assertion the W3 hardening gate needed and the
//   tree did not have — that the SHIPPING root command tree actually
//   carries the surfaces the CLI contract promises.
//
// Why it is written against newRootCmd() and nothing else: every existing
//   test of `run` and of the pbd plugin mounts the thing it tests, so all
//   of them stayed green while the shipped binary had no `cascade run` and
//   no `cascade pbd` at all. A test that supplies its own mount cannot
//   detect a missing mount. This one adds nothing.
// SPORT: cmd/cascade tests (ADD) — P1-E14-W3-S30-T5 (Art.9).

// TestTheShippingTreeCarriesTheContractedSurfaces walks the real root.
func TestTheShippingTreeCarriesTheContractedSurfaces(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()

	for _, path := range [][]string{
		{"run"},
		{"pbd"},
		{"pbd", "summary"}, // the contract's "status"; see status.go on rule R5
		{"pbd", "board"},
		{"pbd", "validate"},
		{"pbd", "lint"},
		{"pbd", "dispatch"},
	} {
		cmd, _, err := root.Find(path)
		if err != nil {
			t.Errorf("the shipping tree has no %q: %v", strings.Join(path, " "), err)
			continue
		}
		if got := cmd.CommandPath(); got != "cascade "+strings.Join(path, " ") {
			t.Errorf("Find(%v) resolved to %q; the command is not actually mounted", path, got)
		}
	}
}

// TestEveryManifestCommandIsMounted holds the namespace to its manifest, so
// a command added to the plugin cannot quietly fail to reach the CLI.
func TestEveryManifestCommandIsMounted(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()

	var specs []plugin.CommandSpec
	for _, reg := range plugin.Builtins() {
		if reg.Manifest.ID == "pbd" {
			specs = reg.Manifest.Provides.Commands
		}
	}
	if len(specs) == 0 {
		t.Fatal("the pbd plugin is not in plugin.Builtins(); its init() never ran in this binary")
	}
	for _, spec := range specs {
		if cmd, _, err := root.Find([]string{"pbd", spec.Name}); err != nil ||
			cmd.CommandPath() != "cascade pbd "+spec.Name {
			t.Errorf("manifest command %q is not mounted on the pbd namespace", spec.Name)
		}
	}
}

// TestAnUnloadableNamespaceRefusesRatherThanVanishing is the Art.1 half. A
// plugin that fails to load must still appear and say why: a namespace that
// silently disappears from the help output is how this defect survived a
// whole wave.
func TestAnUnloadableNamespaceRefusesRatherThanVanishing(t *testing.T) {
	loadErr := errors.New("rejected manifest(s): ghost: bad id")
	cmd := unloadablePluginCmd("ghost", loadErr)

	if cmd.Use != "ghost" {
		t.Errorf("Use = %q, want the namespace to still be present", cmd.Use)
	}
	err := cmd.RunE(cmd, nil)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if !strings.Contains(err.Error(), "bad id") {
		t.Errorf("err = %v, want the load failure's reason carried through", err)
	}
	// And with no load error at all, it still refuses rather than pretending.
	if err := unloadablePluginCmd("ghost", nil).RunE(cmd, nil); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err = %v, want KindUnavailable for an unregistered namespace", err)
	}
}
