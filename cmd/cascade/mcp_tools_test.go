package main

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/internal/runtime"
)

// grantEverything allows every capability. It stands in for an operator
// who has granted them, so these tests assert the WIRING rather than
// whichever grants a machine happens to hold.
type grantEverything struct{}

func (grantEverything) Allow(context.Context, string) bool { return true }

// TestMCPToolWiringServesEverySpecItRegisters is the Art.1 proof at the
// composition root: every tool this build declares has its method really
// bound in the method table the tools dispatch through.
//
// Without it, a spec could name a method nobody registers, and the only
// symptom would be a tool quietly missing from CC's list.
func TestMCPToolWiringServesEverySpecItRegisters(t *testing.T) {
	wiring := buildMCPToolWiring(context.Background(), doctorTestPaths(t), runtime.SystemClock{})
	t.Cleanup(wiring.Close)

	if unservable := coretools.Unservable(wiring.Methods); len(unservable) != 0 {
		t.Fatalf("the composition root serves no method for %v", unservable)
	}
	regs := coretools.Registrations(wiring.Methods)
	specs := coretools.Specs()
	aliases := 0
	for _, s := range specs {
		if s.Alias != "" {
			aliases++
		}
	}
	if len(regs) != len(specs)+aliases {
		t.Fatalf("got %d registrations for %d specs and %d aliases", len(regs), len(specs), aliases)
	}
}

// TestTheDaemonToolRegistryGatesTheFirstPartyTools drives the daemon's own
// registry builder both ways. The pair is the point: a builder that
// ignored its filter would produce the same list twice.
func TestTheDaemonToolRegistryGatesTheFirstPartyTools(t *testing.T) {
	wiring := buildMCPToolWiring(context.Background(), doctorTestPaths(t), runtime.SystemClock{})
	t.Cleanup(wiring.Close)

	granted := daemonMCPToolRegistry(wiring.Methods, grantEverything{})
	listed := map[string]bool{}
	for _, tool := range granted.List() {
		listed[tool.Name] = true
	}
	for _, spec := range coretools.Specs() {
		if !listed[spec.Name] {
			t.Errorf("%q is absent from a fully granted daemon tool registry", spec.Name)
		}
	}
	if !listed["cascade_backup_list"] {
		t.Error("the pre-existing backup tools stopped being registered")
	}

	denied := daemonMCPToolRegistry(wiring.Methods, mcp.DenyAllFilter{})
	for _, tool := range denied.List() {
		for _, spec := range coretools.Specs() {
			if tool.Name == spec.Name {
				t.Errorf("%q was listed by a registry that grants nothing", tool.Name)
			}
		}
	}
	if len(denied.FilteredOut()) == 0 {
		t.Error("a deny-all registry filtered nothing; the filter was never consulted")
	}
}

// TestEveryFirstPartyToolCarriesItsSchema asserts the schemas reach the
// wire descriptor rather than being declared and dropped — the gap
// internal/mcp/initialize.go recorded before this ticket, when every tool
// was emitted with the same permissive object schema.
func TestEveryFirstPartyToolCarriesItsSchema(t *testing.T) {
	wiring := buildMCPToolWiring(context.Background(), doctorTestPaths(t), runtime.SystemClock{})
	t.Cleanup(wiring.Close)

	byName := map[string]mcp.Tool{}
	for _, tool := range daemonMCPToolRegistry(wiring.Methods, grantEverything{}).List() {
		byName[tool.Name] = tool
	}
	for _, spec := range coretools.Specs() {
		tool, ok := byName[spec.Name]
		if !ok {
			t.Fatalf("%q is not registered", spec.Name)
		}
		props, ok := tool.InputSchema["properties"].(map[string]any)
		if !ok || len(props) == 0 {
			t.Errorf("%q reached the registry with no properties in its schema: %v", spec.Name, tool.InputSchema)
		}
	}
}
