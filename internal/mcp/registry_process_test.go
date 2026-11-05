// Purpose: the process-tool exposure rule: a plugin's tools appear only
// when its grants are exactly its non-empty requires, every tool carries
// the plugin prefix and none shadows a builtin, core or earlier process
// tool; one violation excludes the whole plugin; a composite keeps every
// decision the base registry made.
// SPORT: internal/mcp installed process tools tests (P1-PLG-06).
package mcp

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
)

var demoRequires = []string{"net.http:api.example.test", "host.secret_ref"}

// processReg is a process registration for id declaring tools, whose Call
// echoes the tool name it was asked for.
func processReg(id string, grants []string, tools ...string) ProcessToolRegistration {
	m := plugin.Manifest{ID: id, Requires: demoRequires}
	for _, name := range tools {
		m.Provides.Tools = append(m.Provides.Tools, plugin.ToolSpec{Name: name, Description: "d"})
	}
	return ProcessToolRegistration{Manifest: m, Grants: grants,
		Call: func(_ context.Context, tool string, _ []byte) ([]byte, error) { return []byte(tool), nil }}
}

func processSource(regs ...ProcessToolRegistration) ProcessToolSource {
	return func() []ProcessToolRegistration { return regs }
}

func noBuiltins() []plugin.BuiltinRegistration { return nil }

func listedNames(r *ToolRegistry) []string {
	var out []string
	for _, t := range r.List() {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}

func requireListed(t *testing.T, r *ToolRegistry, want ...string) {
	t.Helper()
	sort.Strings(want)
	if got := listedNames(r); !reflect.DeepEqual(got, want) && (len(got) != 0 || len(want) != 0) {
		t.Fatalf("listed %v, want %v", got, want)
	}
}

func TestProcessToolsExposedWhenGrantsEqualRequires(t *testing.T) {
	grants := []string{"host.secret_ref", "net.http:api.example.test", "host.secret_ref"}
	r := NewToolRegistryWithProcess(noBuiltins, processSource(processReg("demo", grants, "demo.a", "demo.b")), nil)
	requireListed(t, r, "demo.a", "demo.b")
	out, err := r.Call(context.Background(), "demo.b", nil)
	if err != nil || string(out) != "demo.b" {
		t.Fatalf("Call(demo.b) = %q, %v; want the plugin reached with its own tool name", out, err)
	}
	for _, tool := range r.List() {
		if tool.PluginID != "demo" {
			t.Fatalf("%s names plugin %q, want demo", tool.Name, tool.PluginID)
		}
	}
	if !ProcessExposable(processReg("demo", grants, "demo.a")) {
		t.Fatal("ProcessExposable refused grants equal to requires")
	}
}

func TestInstalledProcessToolsAbsentWhenGrantsSubsetOfRequires(t *testing.T) {
	for _, grants := range [][]string{nil, {}, {"host.secret_ref"}, {"net.http:api.example.test"}} {
		reg := processReg("demo", grants, "demo.a")
		requireListed(t, NewToolRegistryWithProcess(noBuiltins, processSource(reg), nil))
		if ProcessExposable(reg) {
			t.Fatalf("ProcessExposable admitted grants %v", grants)
		}
	}
}

func TestInstalledProcessToolsAbsentWhenGrantsExceedRequires(t *testing.T) {
	for _, extra := range []string{"oauth:repo", "read", ""} {
		grants := append(append([]string(nil), demoRequires...), extra)
		reg := processReg("demo", grants, "demo.a")
		requireListed(t, NewToolRegistryWithProcess(noBuiltins, processSource(reg), nil))
		if ProcessExposable(reg) {
			t.Fatalf("ProcessExposable admitted the superset %v", grants)
		}
	}
	swapped := processReg("demo", []string{"host.secret_ref", "oauth:repo"}, "demo.a")
	requireListed(t, NewToolRegistryWithProcess(noBuiltins, processSource(swapped), nil))
}

func TestProcessToolsAbsentWhenRequiresEmpty(t *testing.T) {
	for _, requires := range [][]string{nil, {}, {""}, {"host.secret_ref", " "}} {
		reg := processReg("demo", requires, "demo.a")
		reg.Manifest.Requires = requires
		requireListed(t, NewToolRegistryWithProcess(noBuiltins, processSource(reg), nil))
	}
	noCall := processReg("demo", demoRequires, "demo.a")
	noCall.Call = nil
	requireListed(t, NewToolRegistryWithProcess(noBuiltins, processSource(noCall), nil))
	requireListed(t, NewToolRegistryWithProcess(noBuiltins, nil, nil))
}

func TestProcessToolNameOutsidePluginPrefixExcludesPlugin(t *testing.T) {
	for _, bad := range []string{"other.a", "demoa", "demo.", "a.demo.b", "demo.a"} {
		reg := processReg("demo", demoRequires, "demo.ok", bad)
		if bad == "demo.a" {
			reg.Manifest.ID = ""
		}
		requireListed(t, NewToolRegistryWithProcess(noBuiltins, processSource(reg), nil))
	}
	dup := processReg("demo", demoRequires, "demo.a", "demo.a")
	requireListed(t, NewToolRegistryWithProcess(noBuiltins, processSource(dup), nil))
}

func TestProcessToolNameCollisionExcludesPlugin(t *testing.T) {
	builtins := func() []plugin.BuiltinRegistration {
		return []plugin.BuiltinRegistration{reg("b1", []string{"read"}, "demo.read"), reg("b2", []string{"write"}, "demo.hidden")}
	}
	core := []CoreRegistration{
		coreReg("demo.core", ""),
		coreReg("demo.gated", "cap.denied"),
	}
	for _, clash := range []string{"demo.read", "demo.hidden", "demo.core", "demo.gated"} {
		r := NewToolRegistryWithProcess(builtins, processSource(processReg("demo", demoRequires, "demo.own", clash)), nil, core...)
		requireListed(t, r, "demo.read", "demo.core")
		out, err := r.Call(context.Background(), "demo.core", nil)
		if err != nil || string(out) != "core" {
			t.Fatalf("a colliding plugin changed the core tool: %q, %v", out, err)
		}
	}
	first := processReg("demo", demoRequires, "demo.a")
	second := processReg("demo", demoRequires, "demo.b", "demo.a")
	requireListed(t, NewToolRegistryWithProcess(noBuiltins, processSource(first, second), nil), "demo.a")
}

func coreReg(name, capability string) CoreRegistration {
	return CoreRegistration{Tool: Tool{Name: name}, Grants: []string{"read"}, RequiredCapability: capability,
		Handler: func(context.Context, []byte) ([]byte, error) { return []byte("core"), nil }}
}

func TestToolRegistryCompositeSourceKeepsExposureFilter(t *testing.T) {
	builtins := func() []plugin.BuiltinRegistration {
		return []plugin.BuiltinRegistration{reg("b1", []string{"read"}, "b1.tool"), reg("b2", []string{"admin"}, "b2.tool")}
	}
	base := NewToolRegistry(builtins, nil, coreReg("core.open", ""), coreReg("core.gated", "cap.denied"))
	requireListed(t, base, "b1.tool", "core.open")
	composite := base.WithProcess(processSource(
		processReg("demo", demoRequires, "demo.a"),
		processReg("over", demoRequires[:1], "over.a"),
		processReg("core", demoRequires, "core.gated"),
		processReg("b2", demoRequires, "b2.tool"),
	))
	requireListed(t, composite, "b1.tool", "core.open", "demo.a")
	requireListed(t, base, "b1.tool", "core.open")
	if got := composite.FilteredOut(); !reflect.DeepEqual(got, []string{"core.gated"}) {
		t.Fatalf("composite FilteredOut = %v, want the base's [core.gated]", got)
	}
	if _, err := composite.Call(context.Background(), "core.gated", nil); err == nil {
		t.Fatal("a filtered core tool became callable through the composite")
	}
	requireListed(t, base.WithProcess(nil), "b1.tool", "core.open")
}
